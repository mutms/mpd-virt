package host

import (
	"strings"
	"testing"
)

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
