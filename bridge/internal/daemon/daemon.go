// Package daemon glues the rest of the internal packages together
// into a single long-running process.
//
//  1. Read config + state.
//  2. Build oauth.Source (one shelled `gog auth tokens export`).
//  3. Build api.Client + policy.Store + matcher.Matcher.
//  4. First-time-only: shell `gog gmail watch start --topic=...` so
//     Gmail starts publishing pushes to the user's Pub/Sub topic.
//  5. Sync the policy synchronously (fail-fast on bad key).
//  6. Launch policy sync goroutine (every 30s).
//  7. Launch Pub/Sub Pull loop in the foreground until ctx is
//     cancelled by SIGTERM/SIGINT.
//  8. On shutdown: stop sync goroutine, save state, close logger.
package daemon

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/api"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/config"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/forward"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/gogcli"
	mmblog "github.com/theinventor/monstermailbox-cli/bridge/internal/log"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/matcher"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/oauth"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/policy"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/pubsub"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/state"
)

const (
	pidFileName = "bridge.pid"
)

// PidFilePath returns the absolute pid-file location (~/.mmb-bridge/bridge.pid).
func PidFilePath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, pidFileName), nil
}

// WritePid stamps the current PID into the pid file at mode 0600.
func WritePid(pid int) error {
	path, err := PidFilePath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o600)
}

// ReadPid returns the PID stamped in the pid file, or os.ErrNotExist
// when the file is absent.
func ReadPid() (int, error) {
	path, err := PidFilePath()
	if err != nil {
		return 0, err
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(string(bytes))
	if err != nil {
		return 0, fmt.Errorf("malformed pid file %s: %w", path, err)
	}
	return pid, nil
}

// RemovePidFile is best-effort; ENOENT is not an error.
func RemovePidFile() error {
	path, err := PidFilePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// IsAlive reports whether `pid` corresponds to a live process. POSIX
// trick: kill -0 succeeds iff the process exists AND we have permission.
func IsAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// Run is the main daemon entry. Blocks until ctx is cancelled.
func Run(ctx context.Context, foreground bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger, err := mmblog.New(foreground, levelFromConfig(cfg.LogLevel))
	if err != nil {
		return err
	}
	defer logger.Close()

	logger.Infof("daemon starting pid=%d agent=%s account=%s api=%s pubsub=%s/%s local_only=%v",
		os.Getpid(), cfg.AgentEmail, cfg.GoogleAccount, cfg.APIBaseURL, cfg.GCPProject, cfg.PubSubSub, cfg.LocalOnly)

	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	oauthSrc, err := oauth.LoadSourceFromGog(ctx, cfg.GoogleAccount)
	if err != nil {
		return fmt.Errorf("load gog oauth: %w", err)
	}
	// Sanity: mint the first token now, so we surface scope/auth
	// problems before entering the pull loop where the user can't see
	// stderr in detached mode.
	if _, err := oauthSrc.Token(ctx); err != nil {
		return fmt.Errorf("first oauth refresh: %w", err)
	}
	logger.Infof("oauth: pubsub access token minted (cached, refreshes on expiry)")

	apiClient := api.New(cfg.APIBaseURL, cfg.APIKey)
	gog := gogcli.New(cfg.GoogleAccount)

	var pol *policy.Store
	var stopSync func()
	if cfg.LocalOnly {
		pol, err = policy.LoadLocal()
		if err != nil {
			return fmt.Errorf("load local whitelist: %w", err)
		}
		snap, _, _ := pol.Snapshot()
		logger.Infof("policy: local-only mode (%d entries)", len(snap.Whitelist))
	} else {
		pol = policy.NewStore()
		var firstErr error
		stopSync, firstErr = pol.Run(ctx, apiClient)
		if firstErr != nil {
			return fmt.Errorf("first policy sync: %w", firstErr)
		}
		snap, _, _ := pol.Snapshot()
		logger.Infof("policy: synced version=%d (%d entries)", snap.Version, len(snap.Whitelist))
	}

	fwd := &forward.Forwarder{
		API:     apiClient,
		Gog:     gog,
		Policy:  pol,
		Matcher: matcher.New(),
		State:   st,
		Logger:  logger,
	}

	sub := pubsub.New(cfg.GCPProject, cfg.PubSubSub, oauthSrc)

	// Stamp pid file (caller in cmd/start.go has already verified no
	// live daemon is running).
	if err := WritePid(os.Getpid()); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}
	defer RemovePidFile()

	// Wire SIGTERM/SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	loopCtx, cancelLoop := context.WithCancel(ctx)
	go func() {
		<-sigCh
		logger.Infof("daemon: received shutdown signal, draining")
		cancelLoop()
	}()

	pullErrors := uint64(0)
	for loopCtx.Err() == nil {
		msgs, err := sub.Pull(loopCtx, 10)
		if err != nil {
			if loopCtx.Err() != nil {
				break
			}
			atomic.AddUint64(&pullErrors, 1)
			logger.Warnf("pubsub pull: %v (errs=%d, backing off 10s)", err, atomic.LoadUint64(&pullErrors))
			select {
			case <-loopCtx.Done():
				break
			case <-time.After(10 * time.Second):
			}
			continue
		}
		if len(msgs) == 0 {
			continue
		}
		ackIDs := make([]string, 0, len(msgs))
		for _, m := range msgs {
			if err := fwd.Handle(loopCtx, m.Payload); err != nil {
				logger.Warnf("handle push msgID=%s: %v", m.AckID, err)
				// Don't ack on handler error — Pub/Sub will redeliver.
				continue
			}
			ackIDs = append(ackIDs, m.AckID)
		}
		if err := sub.Acknowledge(loopCtx, ackIDs); err != nil {
			logger.Warnf("acknowledge: %v", err)
		}
	}

	if stopSync != nil {
		stopSync()
	}
	if err := st.Save(); err != nil {
		logger.Warnf("save state on shutdown: %v", err)
	}
	logger.Infof("daemon: stopped cleanly")
	return nil
}

func levelFromConfig(s string) mmblog.Level {
	switch s {
	case "debug":
		return mmblog.LevelDebug
	case "warn":
		return mmblog.LevelWarn
	case "error":
		return mmblog.LevelError
	default:
		return mmblog.LevelInfo
	}
}
