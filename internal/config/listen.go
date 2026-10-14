package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ValidateListenAddr accepts a loopback TCP address. An empty host would bind
// every interface in Go's net.Listen, so it is rejected. Hostnames other than
// the literal "localhost" are rejected without a DNS lookup.
func ValidateListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("AGT_LISTEN %q is not host:port: %w", addr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("AGT_LISTEN %q has an invalid port", addr)
	}
	if host == "" {
		return fmt.Errorf("AGT_LISTEN %q has an empty host and would bind every interface", addr)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("AGT_LISTEN %q is not a loopback address; use 127.0.0.1, ::1, or localhost", addr)
	}
	return nil
}
