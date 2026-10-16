package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/csy20/agt/internal/profile"
)

var errProfileRequired = errors.New("profile required")

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, " ") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type profileFlags struct {
	profile *string
	workdir *string
	memory  *string
	timeout *int
	pids    *int
	prepare *bool
	args    *stringList
}

func addProfileFlags(fs *flag.FlagSet) profileFlags {
	var p profileFlags
	p.profile = fs.String("profile", "", "python, go, javascript, or typescript")
	p.workdir = fs.String("workdir", ".", "repository-relative working directory")
	p.memory = fs.String("memory", "256m", "container memory: 64m, 128m, 256m, or 512m")
	p.timeout = fs.Int("timeout", 60, "test timeout in seconds, from 1 to 300")
	p.pids = fs.Int("pids", 64, "process limit, from 32 to 256")
	p.prepare = fs.Bool("prepare", false, "request a dependency prepare step")
	p.args = &stringList{}
	fs.Var(p.args, "test-arg", "one test argv element; repeat for each argument")
	return p
}

func runValueFlags(budget bool) map[string]bool {
	flags := map[string]bool{
		"repo": true, "provider": true, "script": true, "server": true,
		"goal": true, "profile": true, "workdir": true, "memory": true,
		"timeout": true, "pids": true, "test-arg": true, "model": true,
		"contract-json": true, "contract-file": true,
	}
	if budget {
		flags["budget-cents"] = true
	}
	return flags
}

func (p profileFlags) request() (profile.Request, error) {
	if *p.prepare {
		return profile.Request{}, fmt.Errorf("these profiles have no prepare step; dependencies are prebuilt and the network stays disabled")
	}
	if strings.TrimSpace(*p.profile) == "" {
		return profile.Request{}, errProfileRequired
	}
	req := profile.Request{
		Name:       *p.profile,
		Argv:       append([]string(nil), (*p.args)...),
		Workdir:    *p.workdir,
		TimeoutSec: *p.timeout,
		Memory:     *p.memory,
		PIDs:       *p.pids,
	}
	if err := profile.Validate(req); err != nil {
		return profile.Request{}, err
	}
	return req, nil
}

func profilePayload(req profile.Request) map[string]any {
	return map[string]any{
		"name":            req.Name,
		"argv":            req.Argv,
		"workdir":         req.Workdir,
		"timeout_seconds": req.TimeoutSec,
		"memory":          req.Memory,
		"pids":            req.PIDs,
	}
}

func printSuggestion(repo string) {
	abs, err := filepath.Abs(repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "no profile suggested: %s\n", err.Error())
		return
	}
	got := profile.Propose(abs)
	if got.Name == "" {
		fmt.Fprintf(os.Stderr, "no profile suggested: %s\n", got.Reason)
		return
	}
	fmt.Fprintf(os.Stderr, "suggested profile: %s (%s)\n", got.Name, got.Reason)
	fmt.Fprintln(os.Stderr, "detection does not start a run")
}

func profileCommand(args []string) int {
	if len(args) == 0 || args[0] != "detect" {
		fmt.Fprintln(os.Stderr, "usage: agt profile detect --repo PATH")
		return 2
	}
	fs := flag.NewFlagSet("profile detect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repo := fs.String("repo", "", "repository to inspect")
	if err := fs.Parse(reorderFlags(args[1:], map[string]bool{"repo": true})); err != nil {
		return 2
	}
	if *repo == "" {
		fmt.Fprintln(os.Stderr, "profile detect requires --repo")
		return 2
	}
	abs, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "--repo is not a directory")
		return 2
	}
	got := profile.Propose(abs)
	if got.Name == "" {
		fmt.Printf("no profile suggested: %s\n", got.Reason)
		return 0
	}
	fmt.Printf("suggested profile: %s\n%s\n", got.Name, got.Reason)
	fmt.Println("pass --profile to choose it; detection does not start a run")
	return 0
}
