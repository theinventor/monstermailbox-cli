package gogcli

import (
	"encoding/base64"
	"strings"
)

// base64Decode decodes Gmail's URL-safe base64 with optional missing
// padding. Tries (in order): URL-encoding, std-encoding, then either
// with padding rewritten.
func base64Decode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	b, err := base64.URLEncoding.DecodeString(s)
	if err == nil {
		return b, nil
	}
	return base64.StdEncoding.DecodeString(s)
}
