package backends

import (
	"testing"

	"github.com/mutms/mpd-virt/go/internal/exec"
)

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

func TestContainerNotFound(t *testing.T) {
	if !containerNotFound(exec.Result{Stderr: "Error: container not found: mpd-181\n"}) {
		t.Error("a missing container should read as not found")
	}
	if containerNotFound(exec.Result{Stderr: "Error: XPC connection error: Connection invalid\n"}) {
		t.Error("an unavailable API server is not a missing container")
	}
}

func TestParseContainerVersion(t *testing.T) {
	const out = `[
  {"appName":"container","buildType":"release","commit":"16e8d488","version":"1.7.1-6-g16e8d488"},
  {"appName":"container-apiserver","buildType":"release","commit":"16e8d488","version":"container-apiserver version 1.7.1 (build: release, commit: 16e8d48)"}
]`
	v, ok := parseContainerVersion(out)
	if !ok || v != [3]int{1, 7, 1} {
		t.Errorf("parseContainerVersion = %v, %v; want 1.7.1", v, ok)
	}
	if _, ok := parseContainerVersion(`[{"appName":"container","version":"dev"}]`); ok {
		t.Error("an unparseable version should not parse")
	}
	for _, c := range []struct {
		a, b [3]int
		less bool
	}{
		{[3]int{1, 7, 0}, [3]int{1, 7, 1}, true},
		{[3]int{1, 7, 1}, [3]int{1, 7, 1}, false},
		{[3]int{2, 0, 0}, [3]int{1, 7, 1}, false},
		{[3]int{1, 6, 9}, [3]int{1, 7, 1}, true},
	} {
		if got := versionLess(c.a, c.b); got != c.less {
			t.Errorf("versionLess(%v, %v) = %v", c.a, c.b, got)
		}
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
