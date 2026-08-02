package proxy

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
)

// mockProxy runs a minimal server speaking the control protocol on a temp
// socket, so the client can be tested without a real (privileged) mpd-proxy.
func mockProxy(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "mock.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				dec, enc := json.NewDecoder(c), json.NewEncoder(c)
				for {
					var req Request
					if dec.Decode(&req) != nil {
						return
					}
					var resp Response
					switch req.Op {
					case "pubkey":
						resp = Response{OK: true, Pubkey: "bW9ja2tleQ=="}
					case "add", "remove":
						resp = Response{OK: true}
					case "list":
						resp = Response{OK: true, VMs: []VM{{ID: "137", Endpoint: "192.168.1.141:51820"}}}
					default:
						resp = Response{Error: "unknown op " + req.Op}
					}
					_ = enc.Encode(resp)
				}
			}(conn)
		}
	}()
	return sock
}

func TestClientRoundTrips(t *testing.T) {
	c := New(mockProxy(t))

	pk, err := c.Pubkey()
	if err != nil || pk != "bW9ja2tleQ==" {
		t.Fatalf("Pubkey() = %q, %v", pk, err)
	}
	if err := c.Add(VM{
		ID: "137", PublicKey: "bW9ja2tleQ==", Endpoint: "192.168.1.141:51820",
		AllowedIPs: []string{"10.163.137.0/24"}, Resolver: "10.163.137.1:53",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	vms, err := c.List()
	if err != nil || len(vms) != 1 || vms[0].ID != "137" {
		t.Fatalf("List() = %v, %v", vms, err)
	}
	if err := c.Remove("137"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

// A proxy-side error string must surface as a Go error, not a silent success.
func TestClientSurfacesProxyError(t *testing.T) {
	c := New(mockProxy(t))
	if _, err := c.call(Request{Op: "bogus"}); err == nil {
		t.Fatal("expected an error for an unknown op")
	}
}

// A missing proxy is a clear error, not a panic.
func TestClientProxyNotRunning(t *testing.T) {
	c := New("/tmp/nonexistent-mpd-proxy.sock")
	if _, err := c.Pubkey(); err == nil {
		t.Fatal("expected an error when mpd-proxy is not running")
	}
}
