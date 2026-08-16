package cli

import (
	"strings"
	"testing"
)

// The wg0 public key is the one VM-controlled string handed to the
// root-privileged mpd-proxy — only exactly a key may pass.
func TestValidateWGKey(t *testing.T) {
	if err := validateWGKey("6NB6/mPLDnwJZIrYASFuEgJWmDqEzQZkbwqiR2JbYUM="); err != nil {
		t.Errorf("valid key refused: %v", err)
	}
	for name, bad := range map[string]string{
		"empty":              "",
		"short":              "AAAA",
		"newline injection":  "6NB6/mPLDnwJZIrYASFuEgJWmDqEzQZkbwqiR2JbYUM=\nevil",
		"not base64":         strings.Repeat("!", 44),
		"error message":      "wg: interface wg0 does not exist..............",
		"right length wrong": strings.Repeat("A", 43) + "!",
	} {
		if err := validateWGKey(bad); err == nil {
			t.Errorf("%s: %q should have been refused", name, bad)
		}
	}
}
