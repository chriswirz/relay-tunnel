package signal

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A name is chosen by the peer, so it is untrusted input that ends up in log
// lines and in the admin interface.
func TestSanitizeName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"keeps an ordinary name", "workshop-pi", "workshop-pi"},
		{"keeps unicode", "café-hôte", "café-hôte"},
		{"trims and collapses whitespace", "  lab   pi \t 2 ", "lab pi 2"},
		{"flattens newlines that could forge a log line", "pi\npeer joined code=other", "pi peer joined code=other"},
		{"drops other control characters", "pi\x00\x07\x1b[31m", "pi[31m"},
		{"keeps nothing from an empty name", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeName(c.in); got != c.want {
				t.Errorf("sanitizeName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The cap has to hold on a rune boundary, or a truncated multi-byte character
// renders as replacement junk in the interface.
func TestSanitizeNameCaps(t *testing.T) {
	long := sanitizeName(strings.Repeat("a", maxNameLen*2))
	if len(long) != maxNameLen {
		t.Errorf("sanitizeName capped at %d bytes, want %d", len(long), maxNameLen)
	}
	// Four-byte runes divide evenly into the cap only if it is cut carefully.
	wide := sanitizeName(strings.Repeat("😀", maxNameLen))
	if len(wide) > maxNameLen {
		t.Errorf("sanitizeName returned %d bytes, over the %d cap", len(wide), maxNameLen)
	}
	if !utf8.ValidString(wide) {
		t.Errorf("sanitizeName produced invalid UTF-8: %q", wide)
	}
}
