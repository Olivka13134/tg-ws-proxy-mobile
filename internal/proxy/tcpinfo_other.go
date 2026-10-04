//go:build !linux && !windows && !darwin

package proxy

import "net"

func probeFor(c net.Conn) tcpProgress { return nil }
