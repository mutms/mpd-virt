// Package proxy is mpd-virt's client for a running mpd-proxy (the separate
// privileged WireGuard + split-DNS helper). mpd-virt derives each VM's overlay
// facts from its id and tells mpd-proxy to add/remove the peer + DNS route;
// mpd-proxy applies them. The wire protocol is newline-delimited JSON over a
// unix socket — see the mpd-proxy repo.
package proxy

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/mutms/mpd-virt/go/internal/paths"
)

// DefaultSocket is where mpd-proxy listens unless told otherwise:
// ~/.mpd-virt/proxy/socket (see paths.ProxySocket).
func DefaultSocket() string { return paths.ProxySocket() }

// Request / Response / VM mirror mpd-proxy's control protocol.
type Request struct {
	Op         string   `json:"op"`
	ID         string   `json:"id,omitempty"`
	PublicKey  string   `json:"public_key,omitempty"`
	Endpoint   string   `json:"endpoint,omitempty"`
	AllowedIPs []string `json:"allowed_ips,omitempty"`
	Resolver   string   `json:"resolver,omitempty"`
}

type Response struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Pubkey string `json:"pubkey,omitempty"`
	VMs    []VM   `json:"vms,omitempty"`
}

type VM struct {
	ID         string   `json:"id"`
	PublicKey  string   `json:"public_key"`
	Endpoint   string   `json:"endpoint"`
	AllowedIPs []string `json:"allowed_ips"`
	Resolver   string   `json:"resolver"`
}

// Client talks to mpd-proxy over its control socket. The zero value is not
// usable; call New.
type Client struct {
	socket string
}

// New returns a Client for the socket (DefaultSocket if empty).
func New(socket string) *Client {
	if socket == "" {
		socket = DefaultSocket()
	}
	return &Client{socket: socket}
}

// call sends one request and returns the response, turning a proxy-side error
// string into a Go error.
func (c *Client) call(req Request) (Response, error) {
	conn, err := net.Dial("unix", c.socket)
	if err != nil {
		return Response{}, fmt.Errorf("mpd-proxy not reachable at %s (is it running?): %w", c.socket, err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("reading mpd-proxy reply: %w", err)
	}
	if resp.Error != "" {
		return resp, fmt.Errorf("mpd-proxy: %s", resp.Error)
	}
	return resp, nil
}

// Pubkey returns mpd-proxy's own WireGuard public key — the one to authorize on
// each VM's WireGuard endpoint.
func (c *Client) Pubkey() (string, error) {
	r, err := c.call(Request{Op: "pubkey"})
	return r.Pubkey, err
}

// Add upserts a VM's peer and DNS route.
func (c *Client) Add(vm VM) error {
	_, err := c.call(Request{
		Op: "add", ID: vm.ID, PublicKey: vm.PublicKey,
		Endpoint: vm.Endpoint, AllowedIPs: vm.AllowedIPs, Resolver: vm.Resolver,
	})
	return err
}

// Remove drops a VM's peer and DNS route.
func (c *Client) Remove(id string) error {
	_, err := c.call(Request{Op: "remove", ID: id})
	return err
}

// List returns the VMs mpd-proxy currently tunnels.
func (c *Client) List() ([]VM, error) {
	r, err := c.call(Request{Op: "list"})
	return r.VMs, err
}
