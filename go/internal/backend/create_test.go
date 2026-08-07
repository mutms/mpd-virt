package backend

import (
	"strings"
	"testing"
)

// Real /etc/resolv.conf content from an Apple-virtualisation container
// guest, where the runtime writes the file itself and systemd-networkd
// manages nothing.
func TestNameservers(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "apple container guest — the vmnet gateway",
			body: "nameserver 192.168.64.1\n",
			want: []string{"192.168.64.1"},
		},
		{
			name: "several servers keep their order",
			body: "search lan\nnameserver 10.0.0.1\nnameserver 8.8.8.8\n",
			want: []string{"10.0.0.1", "8.8.8.8"},
		},
		{
			// Fields, not a substring search: this line must not be read
			// as configuration.
			name: "a commented server is not configuration",
			body: "# nameserver 1.2.3.4\nnameserver 9.9.9.9\n",
			want: []string{"9.9.9.9"},
		},
		{
			name: "no DNS at all",
			body: "search lan\n",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nameservers(tc.body)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("nameservers = %v, want %v", got, tc.want)
			}
		})
	}
}
