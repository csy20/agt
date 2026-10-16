package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/csy20/agt/internal/client"
	"github.com/csy20/agt/internal/workspace"
)

func modelCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: agt model list|select")
		return 2
	}
	switch args[0] {
	case "list":
		return modelList(args[1:])
	case "select":
		return modelSelect(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown model command %q\n", args[0])
		return 2
	}
}

func modelList(args []string) int {
	fs := flag.NewFlagSet("model list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	asJSON := fs.Bool("json", false, "print the JSON response")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true})); err != nil {
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var body struct {
		Selected string             `json:"selected"`
		Configs  []client.ModelInfo `json:"configs"`
		Error    string             `json:"error"`
	}
	if err := getJSON(base+"/v1/models", &body); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if body.Error != "" {
		fmt.Fprintln(os.Stderr, body.Error)
		return 1
	}
	if *asJSON {
		raw, _ := json.Marshal(body)
		fmt.Println(string(raw))
		fmt.Fprintln(os.Stderr, "No inference request was made.")
		return 0
	}
	if len(body.Configs) == 0 {
		fmt.Println("no model configurations")
		fmt.Println("No inference request was made.")
		return 0
	}
	for _, cfg := range body.Configs {
		mark := " "
		if cfg.Selected {
			mark = "*"
		}
		kind := cfg.Adapter
		if cfg.Scripted {
			kind = "scripted"
		}
		fmt.Printf("%s %s  %s  model %s\n", mark, cfg.Name, kind, emptyModel(cfg.Model))
		if cfg.Endpoint != "" {
			fmt.Printf("  endpoint %s\n", cfg.Endpoint)
		}
		fmt.Printf("  price in %s out %s per million tokens\n", emptyModel(cfg.InputUSDPerM), emptyModel(cfg.OutputUSDPerM))
		if cfg.CredentialEnv != "" {
			state := "unset"
			if cfg.CredentialSet {
				state = "set"
			}
			fmt.Printf("  credential %s %s\n", cfg.CredentialEnv, state)
		}
		if !cfg.Valid {
			fmt.Printf("  invalid: %s\n", cfg.Problem)
		}
	}
	fmt.Println("No inference request was made.")
	return 0
}

func modelSelect(args []string) int {
	fs := flag.NewFlagSet("model select", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	sessionID := fs.String("session", "", "session whose preference to store")
	repoFlag := fs.String("repo", "", "repository bound to the session")
	flags := map[string]bool{"server": true, "session": true, "repo": true}
	if err := fs.Parse(reorderFlags(args, flags)); err != nil {
		return 2
	}
	if len(fs.Args()) != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		fmt.Fprintln(os.Stderr, "usage: agt model select NAME [--session ID] [--repo PATH]")
		return 2
	}
	name := strings.TrimSpace(fs.Arg(0))
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if strings.TrimSpace(*sessionID) == "" {
		var body struct {
			Selected string           `json:"selected"`
			Config   client.ModelInfo `json:"config"`
			Error    string           `json:"error"`
		}
		if err := postJSON(base+"/v1/models/select", map[string]any{"name": name}, &body); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if body.Error != "" {
			fmt.Fprintln(os.Stderr, body.Error)
			return 1
		}
		fmt.Printf("selected %s\n", body.Selected)
		printModelLine(body.Config)
		fmt.Println("No inference request was made.")
		return 0
	}
	payload := map[string]any{"name": name}
	if strings.TrimSpace(*repoFlag) != "" {
		repo, err := workspace.CanonicalRepo(*repoFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		payload["repo_path"] = repo
	}
	var body struct {
		Config client.ModelInfo `json:"config"`
		Error  string           `json:"error"`
	}
	if err := postJSON(base+"/v1/sessions/"+*sessionID+"/model", payload, &body); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if body.Error != "" {
		fmt.Fprintln(os.Stderr, body.Error)
		return 1
	}
	fmt.Printf("session %s\nselected %s\n", *sessionID, body.Config.Name)
	printModelLine(body.Config)
	fmt.Println("No inference request was made.")
	return 0
}

func printModelLine(cfg client.ModelInfo) {
	kind := cfg.Adapter
	if cfg.Scripted {
		kind = "scripted " + kind
	}
	fmt.Printf("%s  model %s\n", kind, emptyModel(cfg.Model))
}

func emptyModel(v string) string {
	if v == "" {
		return "(unset)"
	}
	return v
}

func modelCommands() []string {
	return []string{"list", "select"}
}
