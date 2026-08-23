package vmid

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"100", false}, // bottom of the range
		{"127", false}, // no reserved gaps — any 100-254 is a VM
		{"135", false},
		{"200", false},
		{"254", false}, // top of the octet range
		{"99", true},   // below the range — every id is three digits
		{"5", true},
		{"005", true}, // padding is not a thing: ids have no other spelling
		{"0150", true},
		{"0", true},   // network address, not a VM
		{"255", true}, // broadcast, not a VM
		{"256", true}, // above an octet
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
	if got := id.String(); got != "135" {
		t.Errorf("String() = %q, want 135", got)
	}
	if got := id.Name(); got != "mpd-135" {
		t.Errorf("Name() = %q, want mpd-135", got)
	}
	if got := id.Zone(); got != "135.mpd.test" {
		t.Errorf("Zone() = %q, want 135.mpd.test", got)
	}
}
