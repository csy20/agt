package config

import "testing"

func TestValidateListenAddr(t *testing.T) {
	ok := []string{
		"127.0.0.1:8787",
		"127.0.0.2:1",
		"127.255.255.255:65535",
		"[::1]:8787",
		"localhost:8787",
		"LocalHost:80",
	}
	for _, addr := range ok {
		if err := ValidateListenAddr(addr); err != nil {
			t.Errorf("accept %s: %v", addr, err)
		}
	}
	bad := []string{
		"0.0.0.0:8787",
		"192.168.1.20:8787",
		"10.1.2.3:8787",
		"[::]:8787",
		":8787",
		"example.com:8787",
		"127.0.0.1",
		"",
		"[::1]",
	}
	for _, addr := range bad {
		if err := ValidateListenAddr(addr); err == nil {
			t.Errorf("accepted %q", addr)
		}
	}
}
