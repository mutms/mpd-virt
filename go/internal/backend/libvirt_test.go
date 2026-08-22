package backend

import (
	"strings"
	"testing"
)

func TestLibvirtCanonicalIP(t *testing.T) {
	if got := libvirtCanonicalIP(170); got != "192.168.122.170" {
		t.Errorf("got %q", got)
	}
}

func TestLibvirtNetworkConfig(t *testing.T) {
	cfg := libvirtNetworkConfig("192.168.122.170")
	for _, want := range []string{"driver: virtio_net", "addresses: [192.168.122.170/24]", "gateway4: 192.168.122.1"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("missing %q in:\n%s", want, cfg)
		}
	}
}

// The domain definition names the box, its memory, the two files and the
// default network — what virsh define needs and nothing it would reject.
func TestLibvirtDomainXML(t *testing.T) {
	xml := libvirtDomainXML("mpd-170", 170, 4096, 4, "/var/lib/mpd-virt/mpd-170/disk.qcow2", "/var/lib/mpd-virt/mpd-170/seed.iso")
	for _, want := range []string{
		"<name>mpd-170</name>",
		"<memory unit='MiB'>4096</memory>",
		"<vcpu placement='static'>4</vcpu>",
		"<source file='/var/lib/mpd-virt/mpd-170/disk.qcow2'/>",
		"<source file='/var/lib/mpd-virt/mpd-170/seed.iso'/>",
		"<source network='default'/>",
		"machine='q35'",
		"<mac address='52:54:00:00:00:aa'/>",
		"<timer name='hpet' present='no'/>",
		"<model type='virtio' heads='1'/>", // video: without one the nested guest never boots
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("missing %q", want)
		}
	}
}
