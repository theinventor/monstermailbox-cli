package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/enums"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

// `mmb msg get <id>` → GET /msg/{id}.
//
// Fetching marks the message read by default. `--peek` opts out and
// leaves `read_at` untouched — useful for inspection while preserving
// unread queue semantics.
//
// Verb choice: principle 6 (cross-CLI vocabulary) — `get` is the
// dominant convention for "fetch one by id" across CLI ecosystems.
// `mmb msg show` is preserved as a hidden alias for one release so
// scripts written against the old name keep working; remove in v0.4.
//
// `mmb msg update`, `claim`, `done`, `skip`, `block`, `defer`,
// `awaiting-reply`, and `reopen` all hit PATCH /msg/{id}/work_state.
// `update` is the canonical Trevin-vocabulary form; the others are
// sugar wrappers each agent can type without remembering the work_state
// enum spelling. See newMsgUpdateWorkStateCmd for the shared body
// shape and newMsgWorkStateSugarCmd for how each sugar wrapper maps.
func newMsgCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "msg",
		Short: "Inspect a single message by id, or transition its work_state",
	}
	c.AddCommand(newMsgGetCmd())
	c.AddCommand(newMsgAttachmentCmd())
	c.AddCommand(newMsgShowAliasCmd())
	c.AddCommand(newMsgUpdateWorkStateCmd())
	c.AddCommand(newMsgWorkStateSugarCmd("claim", "in_progress", "inbox",
		"Atomically claim an inbox message — first caller wins, second gets 409"))
	c.AddCommand(newMsgWorkStateSugarCmd("done", "done", "",
		"Mark the message done (work complete; safe terminal state)"))
	c.AddCommand(newMsgWorkStateSugarCmd("skip", "skipped", "",
		"Skip the message — requires --reason / --note explaining why"))
	c.AddCommand(newMsgWorkStateSugarCmd("block", "blocked", "",
		"Mark the message blocked on a human or external dependency — requires --note"))
	c.AddCommand(newMsgWorkStateSugarCmd("defer", "deferred", "",
		"Defer the message; agent will return to it later"))
	c.AddCommand(newMsgWorkStateSugarCmd("awaiting-reply", "awaiting_reply", "",
		"Mark the message as awaiting a sender response (you replied; ball is in their court)"))
	c.AddCommand(newMsgWorkStateSugarCmd("reopen", "inbox", "",
		"Reopen a terminal/long-tail message back to the inbox queue"))
	return c
}

func newMsgAttachmentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "attachment",
		Short: "Download inbound attachments from readable messages",
		Long: `Download inbound attachments from readable messages.

Attachments are untrusted files from email senders. Save them to an explicit
path, then scan or inspect them before opening, executing, importing, or
passing them to another tool.`,
	}
	c.AddCommand(newMsgAttachmentDownloadCmd())
	return c
}

func newMsgAttachmentDownloadCmd() *cobra.Command {
	var output string
	var force bool
	c := &cobra.Command{
		Use:   "download <message-id> <attachment-id>",
		Short: "Download one inbound attachment to an explicit safe path",
		Long: `Download one inbound attachment from a trusted or human-released message.

The server enforces message ownership and quarantine boundaries. The CLI writes
only to --output, refuses path traversal, and does not overwrite existing files
unless --force is passed. It never opens downloaded files. Treat the result as
untrusted: scan or inspect it before opening, executing, importing, or passing
it to another tool.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doAttachmentDownload(cmd, args[0], args[1], output, force)
		},
	}
	c.Flags().StringVarP(&output, "output", "o", "", "required: local file path to write; parent directory must already exist")
	c.Flags().BoolVar(&force, "force", false, "overwrite --output if it already exists")
	_ = c.MarkFlagRequired("output")
	return c
}

func doAttachmentDownload(cmd *cobra.Command, messageID, attachmentID, output string, force bool) error {
	output, err := safeOutputPath(output)
	if err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}
	if err := ensureOutputWritable(output, force); err != nil {
		return err
	}

	path := "/msg/" + messageID + "/attachments/" + attachmentID + "/download"
	cli := newAPIClient()
	resp, err := cli.DoWithHeaders(http.MethodGet, path, nil, nil, map[string]string{
		"Accept": "application/octet-stream",
	})
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return passthroughJSON(cmd.OutOrStdout(), resp)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read attachment response: %w", err)
	}

	meta := attachmentDownloadMetadata{
		ID:          resp.Header.Get("X-Mmb-Attachment-Id"),
		Filename:    resp.Header.Get("X-Mmb-Attachment-Filename"),
		ContentType: resp.Header.Get("X-Mmb-Attachment-Content-Type"),
		SizeBytes:   resp.Header.Get("X-Mmb-Attachment-Size-Bytes"),
		SHA256:      resp.Header.Get("X-Mmb-Attachment-Sha256"),
	}
	if err := meta.verify(body); err != nil {
		return err
	}
	if err := writeAttachmentFile(output, body, force); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "saved: %s\n", output)
	fmt.Fprintf(cmd.OutOrStdout(), "attachment_id: %s\n", meta.ID)
	fmt.Fprintf(cmd.OutOrStdout(), "filename: %s\n", meta.Filename)
	fmt.Fprintf(cmd.OutOrStdout(), "content_type: %s\n", meta.ContentType)
	fmt.Fprintf(cmd.OutOrStdout(), "size_bytes: %d\n", len(body))
	fmt.Fprintf(cmd.OutOrStdout(), "sha256: %s\n", sha256Hex(body))
	return nil
}

type attachmentDownloadMetadata struct {
	ID          string
	Filename    string
	ContentType string
	SizeBytes   string
	SHA256      string
}

func (m attachmentDownloadMetadata) verify(body []byte) error {
	if m.SizeBytes != "" {
		want, err := strconv.ParseInt(m.SizeBytes, 10, 64)
		if err != nil {
			return fmt.Errorf("server returned invalid attachment size %q", m.SizeBytes)
		}
		if int64(len(body)) != want {
			return fmt.Errorf("attachment size mismatch: server metadata says %d bytes, downloaded %d bytes", want, len(body))
		}
	}
	if m.SHA256 != "" {
		got := sha256Hex(body)
		if got != strings.ToLower(m.SHA256) {
			return fmt.Errorf("attachment sha256 mismatch: server metadata says %s, downloaded %s", m.SHA256, got)
		}
	}
	return nil
}

func safeOutputPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("--output is required")
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return "", fmt.Errorf("--output must not contain '..' path traversal")
		}
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == string(filepath.Separator) {
		return "", fmt.Errorf("--output must name a file, not a directory")
	}
	parent := filepath.Dir(clean)
	info, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("output parent directory is not available: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("output parent is not a directory: %s", parent)
	}
	return clean, nil
}

func ensureOutputWritable(path string, force bool) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("--output must not be a symlink: %s", path)
	}
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("--output must name a file, not a directory")
		}
		if !force {
			return fmt.Errorf("output file already exists: %s (pass --force to overwrite)", path)
		}
		return nil
	}
	if os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("stat output file: %w", err)
}

func writeAttachmentFile(path string, body []byte, force bool) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("--output must not be a symlink: %s", path)
	}
	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("output file already exists: %s (pass --force to overwrite)", path)
		}
		return fmt.Errorf("open output file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("write output file: %w", err)
	}
	return nil
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func newMsgGetCmd() *cobra.Command {
	var peek bool
	c := &cobra.Command{
		Use:   "get <id>",
		Short: "Get the full sanitized message JSON for <id>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := newAPIClient()
			q := url.Values{}
			if peek {
				q.Set("peek", "true")
			}
			resp, err := cli.Do(http.MethodGet, "/msg/"+args[0], nil, q)
			if err != nil {
				return fmt.Errorf("GET /msg/%s: %w", args[0], err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().BoolVar(&peek, "peek", false, "do NOT mark the message as read")
	return c
}

// newMsgShowAliasCmd is the deprecated v0.2 spelling. Hidden from
// --help so agents discover only the canonical `msg get`.
func newMsgShowAliasCmd() *cobra.Command {
	var peek bool
	c := &cobra.Command{
		Use:        "show <id>",
		Short:      "(deprecated alias for `msg get`)",
		Args:       cobra.ExactArgs(1),
		Hidden:     true,
		Deprecated: "use `mmb msg get` instead",
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := newAPIClient()
			q := url.Values{}
			if peek {
				q.Set("peek", "true")
			}
			resp, err := cli.Do(http.MethodGet, "/msg/"+args[0], nil, q)
			if err != nil {
				return fmt.Errorf("GET /msg/%s: %w", args[0], err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().BoolVar(&peek, "peek", false, "do NOT mark the message as read")
	return c
}

// `mmb msg update <id> --work-state <state>` is the canonical
// PATCH /msg/{id}/work_state shape, per principle 6 (cross-CLI
// vocabulary: `update`). The sugar wrappers (claim/done/skip/...) all
// dispatch into this same body construction; this command is what
// agent-context surfaces as the "real" verb.
//
// Body fields:
//
//	state                  required; one of enums.WorkStates
//	expected_current_state optional; when set, server only commits if
//	                       the row's current state matches — gives
//	                       caller a race-free "I think it's X, change
//	                       it to Y" handshake.
//	note                   optional; free-text. For state=skipped this
//	                       lands as the skip reason; for state=blocked
//	                       as the blocker note.
//	claimed_by             optional; loop/worker id stamped onto the
//	                       row when state=in_progress.
//
// The endpoint is a mutation, so --idempotency-key + --dry-run are
// available via bindMutationFlags (principle 4).
func newMsgUpdateWorkStateCmd() *cobra.Command {
	var workState, expectedCurrent, note, claimedBy string
	var mf mutationFlags
	c := &cobra.Command{
		Use:   "update <id>",
		Short: "Transition a message's agent-side work_state (PATCH /msg/{id}/work_state)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doWorkStateUpdate(cmd, args[0], workState, expectedCurrent, note, claimedBy, mf)
		},
	}
	c.Flags().StringVar(&workState, "work-state", "",
		fmt.Sprintf("required: target work_state (one of: %s)", joinEnum(enums.WorkStates)))
	c.Flags().StringVar(&expectedCurrent, "expected-current-state", "",
		fmt.Sprintf("optional: only commit if current state matches (one of: %s); 409 on mismatch", joinEnum(enums.WorkStates)))
	c.Flags().StringVar(&note, "note", "", "optional: free-text note (skip reason / block note / completion summary)")
	c.Flags().StringVar(&claimedBy, "claimed-by", "", "optional: loop/worker id stamped on the row when --work-state=in_progress")
	bindMutationFlags(c, &mf)
	return c
}

// newMsgWorkStateSugarCmd builds a thin convenience wrapper around
// the canonical update verb. The sugar commands exist because
// `mmb msg done <id> --note "replied OK"` reads better in an agent
// loop than the `update --work-state done --note ...` form, and
// `mmb msg claim <id>` is a critical race-free verb worth its own
// surface.
//
// `defaultExpected` is the only-when-non-empty
// `expected_current_state` value the wrapper enforces (currently only
// `claim` uses it — claim from anywhere other than :inbox is wrong by
// definition). Other wrappers leave it empty so the server's
// transition table is the single source of truth.
func newMsgWorkStateSugarCmd(verb, targetState, defaultExpected, summary string) *cobra.Command {
	var note, claimedBy, expectedCurrent string
	var reason string // skip-only alias
	var mf mutationFlags
	c := &cobra.Command{
		Use:   verb + " <id>",
		Short: summary,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// `skip` lets the caller pass either --reason or --note;
			// reason is the more natural verb here. We collapse the
			// two onto a single body field so the server-side handler
			// stays simple.
			effectiveNote := note
			if verb == "skip" && reason != "" {
				effectiveNote = reason
			}

			expected := expectedCurrent
			if expected == "" {
				expected = defaultExpected
			}

			return doWorkStateUpdate(cmd, args[0], targetState, expected, effectiveNote, claimedBy, mf)
		},
	}
	c.Flags().StringVar(&note, "note", "",
		"optional: free-text note recorded with the transition")
	if verb == "skip" {
		c.Flags().StringVar(&reason, "reason", "",
			"why you're skipping; takes precedence over --note for skip")
	}
	c.Flags().StringVar(&claimedBy, "claimed-by", "",
		"optional: loop/worker id (only meaningful when claiming or in_progress)")
	c.Flags().StringVar(&expectedCurrent, "expected-current-state", "",
		fmt.Sprintf("optional: only commit if current state matches (one of: %s)", joinEnum(enums.WorkStates)))
	bindMutationFlags(c, &mf)
	return c
}

// doWorkStateUpdate is the shared body for `update` + every sugar
// wrapper. Validates the enum values up front (principle 3 — error
// names the valid set), constructs the request body, and either
// dry-runs or fires the PATCH.
func doWorkStateUpdate(cmd *cobra.Command, id, workState, expected, note, claimedBy string, mf mutationFlags) error {
	if workState == "" {
		return exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("--work-state is required (one of: %s)", joinEnum(enums.WorkStates)))
	}
	if err := enums.Validate("work-state", workState, enums.WorkStates); err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}
	if err := enums.Validate("expected-current-state", expected, enums.WorkStates); err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}

	body := map[string]any{"state": workState}
	if expected != "" {
		body["expected_current_state"] = expected
	}
	if note != "" {
		body["note"] = note
	}
	if claimedBy != "" {
		body["claimed_by"] = claimedBy
	}

	path := "/msg/" + id + "/work_state"

	if mf.DryRun {
		return printJSON(cmd.OutOrStdout(),
			newDryRunEnvelope(http.MethodPatch, path, body, mf))
	}

	cli := newAPIClient()
	resp, err := cli.DoWithHeaders(http.MethodPatch, path, body, nil, mf.Headers())
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", path, err)
	}
	defer resp.Body.Close()
	return passthroughJSON(cmd.OutOrStdout(), resp)
}

// joinEnum returns the comma-separated rendering used in --help text
// and Usage error messages. Pulled out so every flag's description
// uses the same spacing.
func joinEnum(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}
