package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/setup"
	"github.com/csy20/agt/internal/tui"
	"github.com/csy20/agt/internal/workspace"
)

func tuiCmd(args []string) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	demo := fs.Bool("demo", false, "open the example session")
	dump := fs.Bool("dump", false, "print the example frames and exit")
	colorOn := fs.Bool("color", false, "color the dumped frames")
	server := fs.String("server", "", "agtd base URL")
	repoFlag := fs.String("repo", "", "repository for the session")
	providerFlag := fs.String("provider", "", "fake or openai; defaults to the daemon")
	scriptFlag := fs.String("script", "", "fake-provider script, required when the daemon provider is fake")
	sessionFlag := fs.String("session", "", "reopen this session")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"server": true, "repo": true, "provider": true, "script": true, "session": true,
	})); err != nil {
		return 2
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: agt tui [--demo] [--dump] [--repo PATH] [--session ID] [--server URL]")
		return 2
	}
	opts := tui.Options{
		Demo: *demo || *dump, Dump: *dump, Color: *colorOn && os.Getenv("NO_COLOR") == "",
		Provider: *providerFlag, Script: *scriptFlag, Session: *sessionFlag,
		HistoryPath: filepath.Join(config.CurrentPlaces().Cache, "prompt-history"),
		SetupPath:   setup.Path(config.CurrentPlaces().Config),
	}
	if !*dump && !*colorOn {
		opts.Color = colorEnabled()
	}
	if file, err := setup.Load(opts.SetupPath); err == nil {
		opts.Confirmed = &file
	}
	repo := *repoFlag
	if repo == "" {
		if wd, err := os.Getwd(); err == nil {
			repo = wd
		}
	}
	if repo != "" {
		if opts.Dump || opts.Demo {
			abs, err := filepath.Abs(repo)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			opts.Repo = abs
		} else if canonical, err := workspace.CanonicalRepo(repo); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		} else {
			opts.Repo = canonical
		}
	}
	if opts.Dump {
		return tui.Run(opts)
	}
	if !terminalSession() {
		fmt.Fprintln(os.Stderr, "agt needs a terminal. Run `agt tui --dump` to print the example session, or use agt fix.")
		return 2
	}
	if opts.Demo {
		return tui.Run(opts)
	}
	base, err := ensureServer(*server)
	if err != nil {
		opts.DaemonErr = err.Error()
	} else {
		opts.Base = base
	}
	return tui.Run(opts)
}
