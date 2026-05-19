package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theinventor/monstermailbox-cli/internal/client"
)

func runAttachmentDownload(t *testing.T, argv []string, status int, body []byte, headers map[string]string) (string, string, *captured, error) {
	t.Helper()

	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.hits++
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.rawQuery = r.URL.RawQuery
		cap.authHeader = r.Header.Get("Authorization")
		cap.contentType = r.Header.Get("Content-Type")
		cap.body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()

		for k, v := range headers {
			w.Header().Set(k, v)
		}
		if status >= 400 && w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	t.Setenv(client.EnvAPIURL, srv.URL)
	t.Setenv(client.EnvAPIKey, "mmb_testkey1234567890")

	root := NewRootCmd()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(argv)

	err := root.Execute()
	if err != nil {
		_, _ = stderr.WriteString("mmb: " + err.Error() + "\n")
	}
	return stdout.String(), stderr.String(), cap, err
}

func downloadHeaders(body []byte) map[string]string {
	sum := sha256.Sum256(body)
	return map[string]string{
		"Content-Type":                  "application/pdf",
		"X-Mmb-Attachment-Id":           "att_9",
		"X-Mmb-Attachment-Filename":     "report.pdf",
		"X-Mmb-Attachment-Content-Type": "application/pdf",
		"X-Mmb-Attachment-Size-Bytes":   fmt.Sprintf("%d", len(body)),
		"X-Mmb-Attachment-Sha256":       hex.EncodeToString(sum[:]),
		"Content-Disposition":           `attachment; filename="report.pdf"`,
	}
}

func TestMsgAttachmentDownloadWritesBytesAndReportsMetadata(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "report.pdf")
	body := []byte("%PDF-1.4\n")

	stdout, _, cap, err := runAttachmentDownload(t,
		[]string{"msg", "attachment", "download", "123", "att_9", "--output", out},
		http.StatusOK, body, downloadHeaders(body))
	if err != nil {
		t.Fatalf("download returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/msg/123/attachments/att_9/download" {
		t.Fatalf("expected GET download endpoint; got %s %s", cap.method, cap.path)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("output bytes mismatch: got %q want %q", string(got), string(body))
	}
	for _, want := range []string{"saved:", "filename: report.pdf", "content_type: application/pdf", "size_bytes: 9", "sha256:"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout should report %q; got %q", want, stdout)
		}
	}
}

func TestMsgAttachmentDownloadRefusesExistingOutputWithoutForce(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(out, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	_, stderr, cap, err := runAttachmentDownload(t,
		[]string{"msg", "attachment", "download", "123", "att_9", "--output", out},
		http.StatusOK, []byte("new"), downloadHeaders([]byte("new")))
	if err == nil {
		t.Fatalf("download should fail when output exists")
	}
	if cap.hits != 0 {
		t.Fatalf("existing output should fail before HTTP request; got hits=%d", cap.hits)
	}
	if got, _ := os.ReadFile(out); string(got) != "keep" {
		t.Fatalf("existing file should remain unchanged; got %q", string(got))
	}
	if !strings.Contains(stderr, "already exists") || !strings.Contains(stderr, "--force") {
		t.Fatalf("stderr should explain overwrite rule; got %q", stderr)
	}
}

func TestMsgAttachmentDownloadForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(out, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}

	_, _, _, err := runAttachmentDownload(t,
		[]string{"msg", "attachment", "download", "123", "att_9", "--output", out, "--force"},
		http.StatusOK, []byte("new"), downloadHeaders([]byte("new")))
	if err != nil {
		t.Fatalf("download --force returned error: %v", err)
	}
	if got, _ := os.ReadFile(out); string(got) != "new" {
		t.Fatalf("force should replace output; got %q", string(got))
	}
}

func TestMsgAttachmentDownloadRefusesSymlinkOutputEvenWithForce(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.pdf")
	out := filepath.Join(dir, "link.pdf")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, out); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, stderr, cap, err := runAttachmentDownload(t,
		[]string{"msg", "attachment", "download", "123", "att_9", "--output", out, "--force"},
		http.StatusOK, []byte("new"), downloadHeaders([]byte("new")))
	if err == nil {
		t.Fatalf("symlink output should fail")
	}
	if cap.hits != 0 {
		t.Fatalf("symlink output should fail before HTTP request; got hits=%d", cap.hits)
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Fatalf("symlink target should remain unchanged; got %q", string(got))
	}
	if !strings.Contains(stderr, "symlink") {
		t.Fatalf("stderr should explain symlink rule; got %q", stderr)
	}
}

func TestMsgAttachmentDownloadRefusesTraversalBeforeRequest(t *testing.T) {
	dir := t.TempDir()
	out := dir + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "escape.pdf"

	_, stderr, cap, err := runAttachmentDownload(t,
		[]string{"msg", "attachment", "download", "123", "att_9", "--output", out},
		http.StatusOK, []byte("x"), downloadHeaders([]byte("x")))
	if err == nil {
		t.Fatalf("traversal output should fail")
	}
	if cap.hits != 0 {
		t.Fatalf("unsafe output path should not make an HTTP request; hits=%d", cap.hits)
	}
	if !strings.Contains(stderr, "path traversal") {
		t.Fatalf("stderr should explain traversal rule; got %q", stderr)
	}
}

func TestMsgAttachmentDownloadNon2xxWritesNoBytes(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "blocked.pdf")

	stdout, _, cap, err := runAttachmentDownload(t,
		[]string{"msg", "attachment", "download", "123", "att_9", "--output", out},
		http.StatusForbidden, []byte(`{"error":"attachment_unavailable"}`), nil)
	if err == nil {
		t.Fatalf("403 should return an error")
	}
	if cap.path != "/msg/123/attachments/att_9/download" {
		t.Fatalf("unexpected path %s", cap.path)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("failed download must not create output; stat err=%v", statErr)
	}
	if !strings.Contains(stdout, "attachment_unavailable") {
		t.Fatalf("server error body should be visible; stdout=%q", stdout)
	}
}

func TestMsgAttachmentDownloadChecksumMismatchWritesNoBytes(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "bad.pdf")
	headers := downloadHeaders([]byte("expected"))
	headers["X-Mmb-Attachment-Sha256"] = strings.Repeat("0", 64)

	_, stderr, _, err := runAttachmentDownload(t,
		[]string{"msg", "attachment", "download", "123", "att_9", "--output", out},
		http.StatusOK, []byte("expected"), headers)
	if err == nil {
		t.Fatalf("checksum mismatch should fail")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("checksum failure must not create output; stat err=%v", statErr)
	}
	if !strings.Contains(stderr, "sha256 mismatch") {
		t.Fatalf("stderr should explain checksum mismatch; got %q", stderr)
	}
}
