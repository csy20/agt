package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/setup"
)

func setupCmd(args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repoFlag := fs.String("repo", ".", "repository to inspect")
	profileFlag := fs.String("profile", "", "python, go, javascript, or typescript")
	confirm := fs.Bool("confirm", false, "write the profile and test command")
	asJSON := fs.Bool("json", false, "print the setup report as JSON")
	server := fs.String("server", "", "agtd base URL")
	argv := &stringList{}
	fs.Var(argv, "test-arg", "one test argv element; repeat for each argument")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"repo": true, "profile": true, "server": true, "test-arg": true,
	})); err != nil {
		return 2
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: agt setup --repo PATH --profile NAME --test-arg ARG --confirm")
		return 2
	}
	repo, err := filepath.Abs(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	suggestion := profile.Propose(repo)
	cfg := config.Load()
	base, _ := clientBase(*server)
	report := makeDoctorReport(cfg, base, liveDoctor)
	chosen := *profileFlag
	if chosen == "" {
		chosen = suggestion.Name
	}
	out := setupReport{
		DoctorOK:   report.OK,
		Next:       report.Next,
		Suggestion: suggestion.Name,
		Reason:     suggestion.Reason,
		Profile:    chosen,
		Argv:       append([]string(nil), (*argv)...),
		Path:       setup.Path(config.CurrentPlaces().Config),
	}
	if *confirm {
		file := setup.File{Profile: chosen, Argv: out.Argv, Workdir: ".", Repo: repo}
		if err := setup.Save(out.Path, file); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		out.Confirmed = true
		out.OK = true
	}
	if *asJSON {
		raw, err := json.Marshal(out)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(string(raw))
	} else {
		if !report.OK {
			fmt.Print(formatDoctor(report))
		}
		if suggestion.Name != "" {
			fmt.Printf("suggestion %s (%s)\n", suggestion.Name, suggestion.Reason)
		} else if suggestion.Reason != "" {
			fmt.Printf("suggestion none (%s)\n", suggestion.Reason)
		} else {
			fmt.Println("suggestion none")
		}
		fmt.Println("Images stay on the daemon. This command does not start a run.")
		if out.Confirmed {
			fmt.Printf("saved %s\nprofile %s\nargv %s\n", out.Path, out.Profile, joinArgs(out.Argv))
		} else {
			fmt.Println("Confirm the profile and the test command. There is no default test command.")
			fmt.Printf("agt setup --repo %s --profile %s", repo, emptyProfile(chosen))
			for _, arg := range out.Argv {
				fmt.Printf(" --test-arg %s", arg)
			}
			fmt.Println(" --confirm")
		}
	}
	if !*confirm {
		return 2
	}
	return 0
}

type setupReport struct {
	OK         bool     `json:"ok"`
	Confirmed  bool     `json:"confirmed"`
	DoctorOK   bool     `json:"doctor_ok"`
	Next       string   `json:"next,omitempty"`
	Suggestion string   `json:"suggestion,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Profile    string   `json:"profile,omitempty"`
	Argv       []string `json:"argv,omitempty"`
	Path       string   `json:"path,omitempty"`
}

func joinArgs(argv []string) string {
	out := ""
	for i, arg := range argv {
		if i > 0 {
			out += " "
		}
		out += arg
	}
	return out
}

func emptyProfile(name string) string {
	if name == "" {
		return "NAME"
	}
	return name
}
