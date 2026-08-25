package backends

import "testing"

// inspectFixture is real `container inspect mpd-181` output (trimmed): the live
// address is under status.networks[].ipv4Address in CIDR form, and the state
// sits next to it.
const inspectFixture = `[
  {
    "configuration" : { "id" : "mpd-181", "networks" : [ { "network" : "default" } ] },
    "id" : "mpd-181",
    "status" : {
      "networks" : [ { "ipv4Address" : "192.168.64.26/24", "network" : "default" } ],
      "state" : "running"
    }
  }
]`

func TestParseContainerIP(t *testing.T) {
	got, err := parseContainerIP(inspectFixture)
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.168.64.26" {
		t.Errorf("parseContainerIP = %q, want 192.168.64.26 (from status, mask stripped)", got)
	}
}

func TestParseContainerState(t *testing.T) {
	if got := parseContainerState(inspectFixture); got != "running" {
		t.Errorf("parseContainerState = %q, want running", got)
	}
	if got := parseContainerState("not json"); got != "" {
		t.Errorf("unparseable output should read as no state, got %q", got)
	}
}
