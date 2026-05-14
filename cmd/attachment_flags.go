package cmd

import (
	"encoding/base64"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

const (
	maxAttachmentBytes      int64 = 20 * 1024 * 1024
	maxTotalAttachmentBytes int64 = 25 * 1024 * 1024
)

var blockedAttachmentExtensions = map[string]struct{}{
	".app": {}, ".bat": {}, ".bin": {}, ".cmd": {}, ".com": {}, ".cpl": {},
	".dll": {}, ".dmg": {}, ".exe": {}, ".hta": {}, ".iso": {}, ".jar": {},
	".js": {}, ".jse": {}, ".lnk": {}, ".msi": {}, ".ps1": {}, ".scr": {},
	".sh": {}, ".vbe": {}, ".vbs": {}, ".wsf": {},
}

var archiveAttachmentExtensions = map[string]struct{}{
	".7z": {}, ".bz2": {}, ".gz": {}, ".rar": {}, ".tar": {}, ".tgz": {},
	".xz": {}, ".zip": {},
}

// attachmentFlags is shared by outbound send commands. The server contract is:
// attachments: [{ filename, content_type, size, content_base64 }].
//
// Dry-runs must not print attachment bytes, so callers should use the
// metadata-only dry-run payload instead of the live payload.
type attachmentFlags struct {
	Paths []string
}

type outboundAttachment struct {
	Filename      string `json:"filename"`
	ContentType   string `json:"content_type"`
	Size          int64  `json:"size"`
	ContentBase64 string `json:"content_base64"`
}

type attachmentSummary struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

func bindAttachmentFlags(c *cobra.Command, af *attachmentFlags) {
	c.Flags().StringArrayVar(&af.Paths, "attach", nil, "attach a local file; repeat for multiple attachments")
	c.Flags().StringArrayVar(&af.Paths, "attachment", nil, "alias for --attach")
}

func (af attachmentFlags) resolve() ([]outboundAttachment, error) {
	if len(af.Paths) == 0 {
		return nil, nil
	}

	attachments := make([]outboundAttachment, 0, len(af.Paths))
	var total int64
	for _, path := range af.Paths {
		attachment, err := readAttachment(path)
		if err != nil {
			return nil, err
		}
		total += attachment.Size
		if total > maxTotalAttachmentBytes {
			return nil, exitcode.Wrap(exitcode.Usage,
				fmt.Errorf("attachments exceed total size limit of %d bytes", maxTotalAttachmentBytes))
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

func readAttachment(path string) (outboundAttachment, error) {
	filename := filepath.Base(path)
	if err := validateAttachmentFilename(filename); err != nil {
		return outboundAttachment{}, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return outboundAttachment{}, exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("read attachment %s: %w", filename, err))
	}
	if info.IsDir() {
		return outboundAttachment{}, exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("attachment %s is a directory; pass a file path", filename))
	}
	if info.Size() > maxAttachmentBytes {
		return outboundAttachment{}, exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("attachment %s exceeds size limit of %d bytes", filename, maxAttachmentBytes))
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return outboundAttachment{}, exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("read attachment %s: %w", filename, err))
	}

	return outboundAttachment{
		Filename:      filename,
		ContentType:   detectAttachmentContentType(filename, raw),
		Size:          int64(len(raw)),
		ContentBase64: base64.StdEncoding.EncodeToString(raw),
	}, nil
}

func validateAttachmentFilename(filename string) error {
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		return exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("attachment filename is empty; pass a normal file path"))
	}
	if filename != filepath.Base(filename) || strings.ContainsAny(filename, `/\`) {
		return exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("attachment filename %q is unsafe; pass a local file with no path separators in its name", filename))
	}
	for _, r := range filename {
		if unicode.IsControl(r) {
			return exitcode.Wrap(exitcode.Usage,
				fmt.Errorf("attachment filename %q contains control characters", filename))
		}
	}

	exts := attachmentExtensions(filename)
	if len(exts) == 0 {
		return nil
	}
	last := exts[len(exts)-1]
	if _, blocked := blockedAttachmentExtensions[last]; blocked {
		return exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("attachment %s has blocked file extension %s", filename, last))
	}
	if hasNestedArchiveExtensions(exts) {
		return exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("attachment %s looks like an archive inside an archive; send a single archive file instead", filename))
	}
	for _, ext := range exts[:len(exts)-1] {
		if _, blocked := blockedAttachmentExtensions[ext]; blocked {
			return exitcode.Wrap(exitcode.Usage,
				fmt.Errorf("attachment %s has unsafe double extension %s", filename, ext))
		}
	}
	return nil
}

func attachmentExtensions(filename string) []string {
	parts := strings.Split(filename, ".")
	if len(parts) < 2 {
		return nil
	}
	exts := make([]string, 0, len(parts)-1)
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		exts = append(exts, "."+strings.ToLower(part))
	}
	return exts
}

func hasNestedArchiveExtensions(exts []string) bool {
	if len(exts) == 2 && exts[0] == ".tar" {
		switch exts[1] {
		case ".bz2", ".gz", ".xz":
			return false
		}
	}
	archives := 0
	for _, ext := range exts {
		if _, ok := archiveAttachmentExtensions[ext]; ok {
			archives++
		}
	}
	return archives > 1
}

func detectAttachmentContentType(filename string, raw []byte) string {
	if extType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); extType != "" {
		return extType
	}
	if len(raw) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(raw)
}

func attachPayload(payload map[string]any, attachments []outboundAttachment) {
	if len(attachments) > 0 {
		payload["attachments"] = attachments
	}
}

func attachDryRunPayload(payload map[string]any, attachments []outboundAttachment) {
	if len(attachments) == 0 {
		return
	}
	summaries := make([]attachmentSummary, 0, len(attachments))
	for _, a := range attachments {
		summaries = append(summaries, attachmentSummary{
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Size:        a.Size,
		})
	}
	payload["attachments"] = summaries
}
