package cmd

import "testing"

func TestDetectAttachmentContentTypeStripsMimeParameters(t *testing.T) {
	got := detectAttachmentContentType("notes.txt", []byte("plain text"))
	if got != "text/plain" {
		t.Fatalf("text attachments MUST omit MIME parameters for server compatibility; got %q", got)
	}
}

func TestDetectAttachmentContentTypePreservesZipType(t *testing.T) {
	got := detectAttachmentContentType("bundle.zip", []byte("PK\x03\x04"))
	if got != "application/zip" {
		t.Fatalf("zip attachments MUST keep application/zip; got %q", got)
	}
}

func TestNormalizeAttachmentContentTypeFallsBackToBaseType(t *testing.T) {
	got := normalizeAttachmentContentType(" text/plain ; charset=utf-8")
	if got != "text/plain" {
		t.Fatalf("malformed MIME parameters MUST still normalize to base media type; got %q", got)
	}
}
