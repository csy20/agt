package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/csy20/agt/internal/client"
)

// Run starts the session. Dump prints the example frames and returns.
func Run(opts Options) int {
	if opts.Dump {
		fmt.Print(Dump(opts.Color))
		return 0
	}
	if !opts.Demo && !isTerminal() {
		fmt.Fprintln(os.Stderr, "agt needs a terminal. Run `agt tui --dump` to print the example session, or use agt fix.")
		return 2
	}
	if opts.HistoryPath != "" && !opts.Demo && !opts.Dump {
		opts.History = loadHistory(opts.HistoryPath)
		path := opts.HistoryPath
		opts.SaveHist = func(items []string) { _ = saveHistory(path, items) }
	}
	m := newModel(opts)
	if !opts.Demo && opts.Base != "" && opts.DaemonErr == "" {
		c, err := client.Open(opts.Base)
		if err != nil {
			m.opts.DaemonErr = err.Error()
			m.snap.DaemonErr = err.Error()
		} else {
			m.client = c
		}
	}
	program := tea.NewProgram(m, tea.WithAltScreen())
	_, err := program.Run()
	restoreTerminal()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "Left the session. Runs were not cancelled and patches were not applied.")
	return 0
}

func restoreTerminal() {
	_, _ = os.Stdout.Write([]byte("\x1b[?1049l\x1b[?25h\x1b[0m"))
}

func isTerminal() bool {
	info, err := os.Stdout.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	in, err := os.Stdin.Stat()
	return err == nil && in.Mode()&os.ModeCharDevice != 0
}
