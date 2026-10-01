package main

import (
	"context"
	"net"
	"syscall"
	"testing"
)

// TestTCPUserTimeout: a connection of the console gives data a dead peer
// does not acknowledge 30 s, also with data in flight, where keepalive does
// not help.
func TestTCPUserTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := newTCPDialer().DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var v int
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		v, gerr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeout)
	}); err != nil || gerr != nil {
		t.Fatal(err, gerr)
	}
	if v != 30000 {
		t.Errorf("TCP_USER_TIMEOUT = %d, want 30000", v)
	}
}
