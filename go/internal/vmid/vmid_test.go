package vmid

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"005", false}, // padded low id
		{"5", false},   // unpadded input accepted, same id
		{"1", false},   // bottom of the octet range
		{"64", false},
		{"127", false}, // no reserved gaps anymore — any 1-254 is a box
		{"135", false},
		{"200", false},
		{"254", false}, // top of the octet range
		{"0", true},    // network address, not a box
		{"255", true},  // broadcast, not a box
		{"256", true},  // above an octet
		{"260", true},
		{"-1", true},  // negative
		{"abc", true}, // not digits
		{"", true},    // empty
		{"12x", true}, // trailing junk
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
