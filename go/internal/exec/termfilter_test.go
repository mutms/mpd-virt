package exec

import (
	"bytes"
	"testing"
)

// Remote output is hostile terminal input: everything but text and SGR
// colors must be stripped, whole sequences at a time, however the chunks
// fall.
func TestSanitize(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain text passes", "hello mpd-141\n\tok\r", "hello mpd-141\n\tok\r"},
		{"utf-8 passes", "✓ zóna přežila", "✓ zóna přežila"},
		{"SGR colors pass", "\x1b[1;32mok\x1b[0m", "\x1b[1;32mok\x1b[0m"},
		{"cursor movement dropped", "a\x1b[2Ab", "ab"},
		{"screen clear dropped", "\x1b[2Jboo", "boo"},
		{"osc title dropped", "\x1b]0;you are safe\x07text", "text"},
		{"osc52 clipboard write dropped", "\x1b]52;c;ZXZpbA==\x1b\\after", "after"},
		{"dcs dropped", "\x1bPq#evil\x1b\\x", "x"},
		{"two-byte escapes dropped", "\x1b(0line\x1b(B", "line"},
		{"stray C0 dropped", "a\x00b\x08c\x07d", "abcd"},
		{"del dropped", "a\x7fb", "ab"},
		{"non-numeric CSI-m dropped", "\x1b[>4;2mx", "x"},
		{"unterminated sequence swallowed", "ok\x1b]0;half", "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sanitize(tc.in); got != tc.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The streaming writer must behave identically when a sequence is split
// across Write calls — that is how ssh output actually arrives.
func TestFilterWriterSplitSequences(t *testing.T) {
	var out bytes.Buffer
	w := newFilterWriter(&out)
	for _, chunk := range []string{"pre \x1b]52;c;", "ZXZpbA==\x07post \x1b[3", "2mgreen\x1b[0m"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	want := "pre post \x1b[32mgreen\x1b[0m"
	if out.String() != want {
		t.Errorf("streamed = %q, want %q", out.String(), want)
	}
}
