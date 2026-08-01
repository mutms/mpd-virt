package vmid

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
		class   Class
	}{
		{"135", false, General},
		{"128", false, General},
		{"159", false, General},
		{"160", false, Container},
		{"191", false, Container},
		{"200", false, Proxmox},
		{"223", false, Proxmox},
		{"127", true, ""}, // free block below
		{"224", true, ""}, // free block above
		{"42", true, ""},  // well below range
		{"260", true, ""}, // above an octet
		{"abc", true, ""}, // not a number
		{"", true, ""},    // empty
		{"12x", true, ""}, // trailing junk
	}
	for _, c := range cases {
		id, err := Parse(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("Parse(%q): want error, got id %d", c.in, id)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): unexpected error: %v", c.in, err)
			continue
		}
		if id.Class() != c.class {
			t.Errorf("Parse(%q).Class() = %q, want %q", c.in, id.Class(), c.class)
		}
	}
}

func TestDerivations(t *testing.T) {
	id, _ := Parse("135")
	if got := id.Pad(); got != "135" {
		t.Errorf("Pad() = %q, want 135", got)
	}
	if got := id.Name(); got != "mpd-135" {
		t.Errorf("Name() = %q, want mpd-135", got)
	}
	if got := id.Zone(); got != "135.mpd.test" {
		t.Errorf("Zone() = %q, want 135.mpd.test", got)
	}

	// Zero-padding to three digits.
	id, _ = Parse("128")
	if got := id.Name(); got != "mpd-128" {
		t.Errorf("Name() = %q, want mpd-128", got)
	}
}
