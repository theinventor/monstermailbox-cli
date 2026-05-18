package cmd

import (
	"fmt"
	"net/mail"
	"strings"

	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

var blockedOwnerEmailDomains = map[string]struct{}{
	"example.com":  {},
	"example.org":  {},
	"example.net":  {},
	"test.invalid": {},
	"localhost":    {},
	"localdomain":  {},
}

var blockedOwnerEmailLocalParts = map[string]struct{}{
	"noreply":       {},
	"donotreply":    {},
	"notification":  {},
	"notifications": {},
	"automated":     {},
	"bot":           {},
	"robot":         {},
}

const invalidOwnerEmailMessage = "owner email must be a real human owner email, not a placeholder/example or non-human address; use the actual human owner's email"

func validateHumanOwnerEmail(email string) error {
	addr, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil {
		return exitcode.Wrap(exitcode.Validation, fmt.Errorf("owner email must be a valid email address"))
	}
	if addr.Address != strings.TrimSpace(email) || strings.Count(addr.Address, "@") != 1 {
		return exitcode.Wrap(exitcode.Validation, fmt.Errorf("owner email must be a valid email address"))
	}

	parts := strings.Split(addr.Address, "@")
	local := strings.ToLower(strings.TrimSpace(parts[0]))
	domain := strings.ToLower(strings.Trim(strings.TrimSpace(parts[1]), "."))
	if local == "" || domain == "" {
		return exitcode.Wrap(exitcode.Validation, fmt.Errorf("owner email must be a valid email address"))
	}

	if _, blocked := blockedOwnerEmailDomains[domain]; blocked || strings.HasSuffix(domain, ".localhost") || strings.HasSuffix(domain, ".localdomain") {
		return exitcode.Wrap(exitcode.Validation, fmt.Errorf(invalidOwnerEmailMessage))
	}

	baseLocal := strings.Split(local, "+")[0]
	normalizedLocal := strings.NewReplacer("-", "", "_", "", ".", "").Replace(baseLocal)
	if _, blocked := blockedOwnerEmailLocalParts[normalizedLocal]; blocked {
		return exitcode.Wrap(exitcode.Validation, fmt.Errorf(invalidOwnerEmailMessage))
	}

	return nil
}
