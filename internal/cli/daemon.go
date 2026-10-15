package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func daemonBinaryNames(goos string) []string {
	if goos == "windows" {
		return []string{"agtd.exe", "agtd"}
	}
	return []string{"agtd"}
}

func selectDaemon(dir string, names []string) (string, error) {
	tried := make([]string, 0, len(names))
	for _, name := range names {
		cand := filepath.Join(dir, name)
		tried = append(tried, cand)
		st, err := os.Stat(cand)
		if err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	return "", fmt.Errorf("agtd was not found (looked for %s)", strings.Join(tried, ", "))
}

func findDaemonBinary() (string, error) {
	if bin := os.Getenv("AGTD_BIN"); bin != "" {
		st, err := os.Stat(bin)
		if err != nil || st.IsDir() {
			return "", fmt.Errorf("AGTD_BIN=%s is not a file", bin)
		}
		return bin, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate the agt executable: %w", err)
	}
	return selectDaemon(filepath.Dir(exe), daemonBinaryNames(runtime.GOOS))
}
