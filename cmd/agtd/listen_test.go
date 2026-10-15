package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestNonLoopbackListenExits(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:1", "192.168.1.20:1", ":8787"} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv("AGT_LISTEN", addr)
			old := os.Stderr
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stderr = w
			t.Cleanup(func() { os.Stderr = old })
			code := run()
			_ = w.Close()
			os.Stderr = old
			out, _ := io.ReadAll(r)
			_ = r.Close()
			if code == 0 {
				t.Fatal("startup succeeded")
			}
			if !strings.Contains(string(out), "AGT_LISTEN") {
				t.Fatalf("exit %d log %s", code, out)
			}
		})
	}
}
