//go:build !linux

package main

import "syscall"

// tcpControl does nothing outside Linux, which alone has TCP_USER_TIMEOUT
// (tcp_linux.go); keepalive still finds a dead peer of an idle connection.
func tcpControl(network, address string, c syscall.RawConn) error { return nil }
