package main

import "syscall"

// tcpUserTimeout is TCP_USER_TIMEOUT. syscall of Go 1.26 names it for
// linux/arm64 only, not for linux/amd64, so the number stands here.
const tcpUserTimeout = 0x12

// tcpUserTimeoutMs is how long data sent to a dead peer may go unacknowledged
// before the kernel gives the connection up: keepalive alone finds a dead
// peer only while nothing is in flight.
const tcpUserTimeoutMs = 30000

// tcpControl sets TCP_USER_TIMEOUT on the socket of a TCP console connection
// before it connects.
func tcpControl(network, address string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeout, tcpUserTimeoutMs)
	}); err != nil {
		return err
	}
	return serr
}
