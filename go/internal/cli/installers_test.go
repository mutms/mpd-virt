package cli

import "testing"

func TestPlainName(t *testing.T) {
	for name, want := range map[string]bool{
		"goland.tgz": true, "x-1_2.tgz": true,
		".DS_Store": false, "my ide.tgz": false, "a;b.tgz": false, "-x": false, "sub/x.tgz": false,
	} {
		if plainName.MatchString(name) != want {
			t.Errorf("plainName(%q) = %v, want %v", name, !want, want)
		}
	}
}
