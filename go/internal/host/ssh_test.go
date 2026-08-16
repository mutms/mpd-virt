package host

import (
	"strings"
	"testing"
)

// A pinned target must carry its pin on every ssh and scp invocation —
// the managed ~/.ssh/config block matches the box's bare IP, so without
// the explicit options the block's file would win and the pin would not.
func TestArgsCarryHostKeyPin(t *testing.T) {
	pinned := Target{
		User: "dev", Host: "10.1.1.161",
		KnownHostsFile: "/x/161/known_hosts",
		HostKeyAlias:   "mpd-161",
	}
	for name, args := range map[string][]string{
		"ssh": pinned.sshArgs("true"),
		"scp": pinned.scpArgs("/tmp/a", "/tmp/b"),
	} {
		joined := strings.Join(args, " ")
		for _, want := range []string{
			"UserKnownHostsFile=/x/161/known_hosts",
			"HostKeyAlias=mpd-161",
			"StrictHostKeyChecking=accept-new",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("%s args missing %q: %s", name, want, joined)
			}
		}
	}
	// And an unpinned target (LAN probe, pre-create wait) stays on the
	// default known_hosts rather than pinning to an empty path.
	plain := Target{User: "dev", Host: "10.1.1.161"}
	if joined := strings.Join(plain.sshArgs("true"), " "); strings.Contains(joined, "UserKnownHostsFile") {
		t.Errorf("unpinned target must not set UserKnownHostsFile: %s", joined)
	}
}

// The stderr blobs below are real ssh output, captured from OpenSSH on
// Debian Trixie against a live box. They are verbatim on purpose: the
// classification keys off substrings ssh prints, so a paraphrase would
// pin nothing.
func TestClassify(t *testing.T) {
	target := Target{User: "skodak", Host: "10.1.1.161"}

	changedKey := `@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
IT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!
It is also possible that a host key has just been changed.
Host key for 10.1.1.161 has changed and you have requested strict checking.
Host key verification failed.`

	cases := []struct {
		name   string
		detail string
		want   []string
		reject []string
	}{
		{
			name:   "changed host key names the remedy",
			detail: changedKey,
			// The command must be complete enough to paste, and the
			// message must not send the reader to authorized_keys.
			want:   []string{"ssh-keygen -R 10.1.1.161", "known_hosts", "snapshot"},
			reject: []string{"authorized_keys"},
		},
		{
			name:   "unauthorized key points at authorized_keys",
			detail: "skodak@10.1.1.161: Permission denied (publickey,password).",
			want:   []string{"authorized_keys", "skodak@10.1.1.161"},
			reject: []string{"ssh-keygen -R"},
		},
		{
			name:   "no route reads as an unreachable box",
			detail: "ssh: connect to host 10.1.1.161 port 22: No route to host",
			want:   []string{"no ssh answer", "10.1.1.161"},
			reject: []string{"ssh-keygen -R", "authorized_keys"},
		},
		{
			name:   "anything else carries ssh's own words through",
			detail: "ssh: something nobody anticipated",
			want:   []string{"something nobody anticipated"},
		},
		{
			name:   "silence still produces a usable message",
			detail: "",
			want:   []string{"without output"},
		},
	}

	// A pinned target's remedy names the per-box file and the stable alias,
	// so the paste-able command edits the pin and not ~/.ssh/known_hosts.
	t.Run("changed key on a pinned target names the pin", func(t *testing.T) {
		pinned := Target{
			User: "skodak", Host: "10.1.1.161",
			KnownHostsFile: "/Users/skodak/.mpd-virt/161/known_hosts",
			HostKeyAlias:   "mpd-161",
		}
		got := pinned.classify(changedKey).Error()
		for _, want := range []string{
			"ssh-keygen -R mpd-161 -f /Users/skodak/.mpd-virt/161/known_hosts",
			"snapshot",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("message should contain %q:\n%s", want, got)
			}
		}
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := target.classify(tc.detail)
			if err == nil {
				t.Fatal("classify returned nil for a failure")
			}
			got := err.Error()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("message should contain %q:\n%s", want, got)
				}
			}
			for _, reject := range tc.reject {
				if strings.Contains(got, reject) {
					t.Errorf("message should NOT contain %q:\n%s", reject, got)
				}
			}
		})
	}
}
