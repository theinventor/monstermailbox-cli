package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

// bodyFlags is the shared flag set for outbound commands that ship a
// message body (both `new-email` and `reply-to-email`). Mirrors the
// server's POST /send shape:
//
//   • --body            inline plain-text body
//   • --body-html       inline HTML body
//   • --body-file       read --body from a file
//   • --body-html-file  read --body-html from a file
//
// At least one of the four must produce non-empty content. Pairing
// the same body in both inline and file forms is a usage error
// (principle 3 — fail fast with errors that say what to do).
//
// File variants exist because HTML rarely shell-escapes cleanly,
// and either body can be long enough that argv length matters.
type bodyFlags struct {
	Text     string
	HTML     string
	TextFile string
	HTMLFile string
}

func bindBodyFlags(c *cobra.Command, bf *bodyFlags) {
	c.Flags().StringVar(&bf.Text, "body", "", "plain-text body (one of --body / --body-html / --body-file / --body-html-file is required)")
	c.Flags().StringVar(&bf.HTML, "body-html", "", "HTML body — server scans + ships as multipart text/html")
	c.Flags().StringVar(&bf.TextFile, "body-file", "", "read --body from this file path (mutually exclusive with --body)")
	c.Flags().StringVar(&bf.HTMLFile, "body-html-file", "", "read --body-html from this file path (mutually exclusive with --body-html)")
}

// resolveBody validates the flag combination and reads file inputs.
// Returns (text, html) — either may be "" but not both. Wraps every
// validation failure with exitcode.Usage so agents can branch on
// "fix the invocation" without a deeper error chain.
func (bf bodyFlags) resolve() (text string, html string, err error) {
	if bf.Text != "" && bf.TextFile != "" {
		return "", "", exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("--body and --body-file are mutually exclusive — pick one"))
	}
	if bf.HTML != "" && bf.HTMLFile != "" {
		return "", "", exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("--body-html and --body-html-file are mutually exclusive — pick one"))
	}

	text = bf.Text
	html = bf.HTML
	if bf.TextFile != "" {
		raw, ioErr := os.ReadFile(bf.TextFile)
		if ioErr != nil {
			return "", "", exitcode.Wrap(exitcode.Usage,
				fmt.Errorf("read --body-file %s: %w", bf.TextFile, ioErr))
		}
		text = string(raw)
	}
	if bf.HTMLFile != "" {
		raw, ioErr := os.ReadFile(bf.HTMLFile)
		if ioErr != nil {
			return "", "", exitcode.Wrap(exitcode.Usage,
				fmt.Errorf("read --body-html-file %s: %w", bf.HTMLFile, ioErr))
		}
		html = string(raw)
	}

	if text == "" && html == "" {
		return "", "", exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("at least one of --body / --body-html / --body-file / --body-html-file is required"))
	}
	return text, html, nil
}
