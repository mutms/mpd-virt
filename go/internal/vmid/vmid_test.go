package vmid

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
		class   Class
	}{
		{"005", false, Generic}, // padded low id
		{"5", false, Generic},   // unpadded input accepted, same id
		{"1", false, Generic},
		{"64", false, Generic},
		{"135", false, Parallels},
		{"128", false, Parallels},
		{"159", false, Parallels},
		{"160", false, Container},
		{"191", false, Container},
		{"200", false, Proxmox},
		{"223", false, Proxmox},
		{"0", true, ""},   // zero is not a box
		{"65", true, ""},  // reserved gap 065-127
		{"127", true, ""}, // reserved gap
		{"224", true, ""}, // reserved above
		{"260", true, ""}, // above an octet
		{"abc", true, ""}, // not digits
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

	// Low id: unpadded input accepted, identity is always padded.
	id, _ = Parse("5")
	if got := id.Pad(); got != "005" {
		t.Errorf("Pad() = %q, want 005", got)
	}
	if got := id.Name(); got != "mpd-005" {
		t.Errorf("Name() = %q, want mpd-005", got)
	}
	if got := id.Zone(); got != "005.mpd.test" {
		t.Errorf("Zone() = %q, want 005.mpd.test", got)
	}
}
