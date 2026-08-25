package backend

import "testing"

func TestParseSizeMiB(t *testing.T) {
	cases := map[string]int{
		"8g":    8192,
		"8G":    8192,
		"8192m": 8192,
		"8192":  8192,
		" 4g ":  4096,
		"":      0,
		"junk":  0,
	}
	for in, want := range cases {
		if got := ParseSizeMiB(in); got != want {
			t.Errorf("ParseSizeMiB(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseSizeGiB(t *testing.T) {
	cases := map[string]int{
		"80g":    80,
		"80G":    80,
		"81920m": 80,
		"81920":  80, // bare number read as MiB → 80 GiB
		"":       0,
		"nope":   0,
	}
	for in, want := range cases {
		if got := ParseSizeGiB(in); got != want {
			t.Errorf("ParseSizeGiB(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestStripMask(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.64.26/24": "192.168.64.26",
		" 10.1.1.5 ":       "10.1.1.5",
		"-":                "-",
	} {
		if got := StripMask(in); got != want {
			t.Errorf("StripMask(%q) = %q, want %q", in, got, want)
		}
	}
}
