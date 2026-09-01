package backends

import "testing"

const prlctlInfo = `[
  {"ID":"b1c4","Name":"macvm","Description":"mpd-virt dev","State":"suspended"},
  {"ID":"a31f","Name":"mpd-130","Description":"UHK  - bbmig","State":"running"}
]`

func TestParseParallelsNotesPicksTheNamedVM(t *testing.T) {
	if got := parseParallelsNotes(prlctlInfo, "mpd-130"); got != "UHK  - bbmig" {
		t.Errorf("notes = %q, want %q", got, "UHK  - bbmig")
	}
	if got := parseParallelsNotes(prlctlInfo, "macvm"); got != "mpd-virt dev" {
		t.Errorf("other VM = %q", got)
	}
	if got := parseParallelsNotes(prlctlInfo, "mpd-999"); got != "" {
		t.Errorf("unknown VM = %q, want empty", got)
	}
	if got := parseParallelsNotes("not json", "mpd-130"); got != "" {
		t.Errorf("garbage = %q, want empty", got)
	}
}
