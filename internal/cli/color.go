package cli

import (
	"fmt"
	"os"
)

func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return true
}

func writeLine(line string) {
	if line == "" {
		return
	}
	if colorEnabled() && (starts(line, "approval required") || starts(line, "failed:") || starts(line, "verification failed")) {
		fmt.Println("\x1b[31m" + line + "\x1b[0m")
		return
	}
	fmt.Println(line)
}

func starts(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
