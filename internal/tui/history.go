package tui

import (
	"os"
	"path/filepath"
	"strings"
)

func loadHistory(path string) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			out = append(out, line)
		}
	}
	if len(out) > 50 {
		out = out[len(out)-50:]
	}
	return out
}

func saveHistory(path string, items []string) error {
	if path == "" {
		return nil
	}
	if len(items) > 50 {
		items = items[len(items)-50:]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body := strings.Join(items, "\n")
	if body != "" {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
