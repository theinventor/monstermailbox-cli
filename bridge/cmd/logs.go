package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	mmblog "github.com/theinventor/monstermailbox-cli/bridge/internal/log"
)

func newLogsCmd() *cobra.Command {
	var follow bool
	var lines int
	c := &cobra.Command{
		Use:   "logs",
		Short: "Show the bridge daemon's log file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := mmblog.Path()
			if err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("no log yet at %s — has the daemon started?", path)
				}
				return err
			}
			defer f.Close()

			if lines > 0 {
				if err := tailLines(f, lines, cmd.OutOrStdout()); err != nil {
					return err
				}
			} else if !follow {
				if _, err := io.Copy(cmd.OutOrStdout(), f); err != nil {
					return err
				}
			}

			if !follow {
				return nil
			}

			// Follow mode: seek to end, then poll for appended bytes.
			if _, err := f.Seek(0, io.SeekEnd); err != nil {
				return err
			}
			buf := make([]byte, 4096)
			for {
				n, err := f.Read(buf)
				if n > 0 {
					if _, werr := cmd.OutOrStdout().Write(buf[:n]); werr != nil {
						return werr
					}
				}
				if err != nil && err != io.EOF {
					return err
				}
				if n == 0 {
					select {
					case <-cmd.Context().Done():
						return cmd.Context().Err()
					case <-time.After(500 * time.Millisecond):
					}
				}
			}
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "follow appends (like `tail -f`)")
	c.Flags().IntVarP(&lines,  "lines",  "n", 0,     "show only the last N lines")
	return c
}

// tailLines streams the LAST `n` lines of `f` to `out`. Naive
// implementation is fine for log files <100 MB.
func tailLines(f *os.File, n int, out io.Writer) error {
	all, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return nil
	}
	// Walk from the end counting newlines.
	count := 0
	cut := 0
	for i := len(all) - 1; i >= 0; i-- {
		if all[i] == '\n' {
			count++
			if count > n {
				cut = i + 1
				break
			}
		}
	}
	_, err = out.Write(all[cut:])
	return err
}
