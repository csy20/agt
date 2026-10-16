package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/csy20/agt/internal/agent"
)

func agentCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: agt agent list")
		return 2
	}
	switch args[0] {
	case "list":
		return agentList(args[1:])
	case "ask":
		return agentAsk(args[1:])
	case "run":
		fmt.Fprintln(os.Stderr, "agt agent run is unavailable. AGT does not start Codex or Claude. Those CLIs own their subprocesses, files, and network, and this build has no tested mediation for that.")
		return 2
	default:
		fmt.Fprintf(os.Stderr, "unknown agent command %q\n", args[0])
		return 2
	}
}

func agentList(args []string) int {
	fs := flag.NewFlagSet("agent list", flag.ContinueOnError)
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
	var screen agent.Screen
	if err := getJSON(base+"/v1/agents", &screen); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		raw, _ := json.Marshal(screen)
		fmt.Println(string(raw))
		fmt.Fprintln(os.Stderr, screen.Notice)
		return 0
	}
	fmt.Print(agent.FormatScreen(screen))
	return 0
}

func agentAsk(args []string) int {
	fs := flag.NewFlagSet("agent ask", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	repo := fs.String("repo", "", "repository to copy before the ask")
	prompt := fs.String("prompt", "", "read-only question")
	authorize := fs.Bool("authorize-live", false, "allow one read-only Codex app-server ask")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true, "repo": true, "prompt": true})); err != nil {
		return 2
	}
	if !*authorize {
		fmt.Fprintln(os.Stderr, "agt agent ask requires --authorize-live. A saved Codex login is not permission, and this command does not start Codex without that flag.")
		return 2
	}
	if strings.TrimSpace(*repo) == "" || strings.TrimSpace(*prompt) == "" {
		fmt.Fprintln(os.Stderr, "usage: agt agent ask --authorize-live --repo PATH --prompt TEXT [--server URL]")
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var out struct {
		Error       string `json:"error"`
		Label       string `json:"label"`
		Live        bool   `json:"live"`
		TurnStarted bool   `json:"turn_started"`
		PromptSent  bool   `json:"prompt_sent"`
		Result      struct {
			Text           string `json:"text"`
			Backend        string `json:"backend"`
			Outcome        string `json:"outcome"`
			Limitation     string `json:"limitation"`
			RuntimeVersion string `json:"runtime_version"`
			RuntimeDigest  string `json:"runtime_digest"`
			Model          string `json:"model"`
			UsageKnown     bool   `json:"usage_known"`
			InputTokens    int    `json:"input_tokens"`
			OutputTokens   int    `json:"output_tokens"`
			CostKnown      bool   `json:"cost_known"`
		} `json:"result"`
	}
	err = postJSONTimeout(base+"/v1/agents/codex/ask", map[string]any{
		"repo_path": *repo,
		"prompt":    *prompt,
		"authorize": true,
	}, &out, 5*time.Minute)
	fmt.Printf("label %s live %t turn_started %t prompt_sent %t\n", out.Label, out.Live, out.TurnStarted, out.PromptSent)
	if out.Result.Limitation != "" {
		fmt.Printf("limitation %s\n", out.Result.Limitation)
	}
	if out.Result.Text != "" {
		fmt.Println(out.Result.Text)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	if out.Result.RuntimeVersion != "" || out.Result.RuntimeDigest != "" {
		fmt.Fprintf(os.Stderr, "runtime %s %s\n", out.Result.RuntimeVersion, out.Result.RuntimeDigest)
	}
	if out.Result.Model != "" {
		fmt.Fprintf(os.Stderr, "model %s\n", out.Result.Model)
	}
	if out.Result.UsageKnown {
		fmt.Fprintf(os.Stderr, "usage input %d output %d\n", out.Result.InputTokens, out.Result.OutputTokens)
	}
	if err != nil {
		return 1
	}
	if out.Live {
		fmt.Fprintf(os.Stderr, "backend %s outcome %s\n", out.Result.Backend, out.Result.Outcome)
		if !out.Result.CostKnown {
			fmt.Fprintln(os.Stderr, "cost unknown")
		}
	} else {
		fmt.Fprintln(os.Stderr, "This result is not a live Codex turn.")
	}
	return 0
}

func agentCommands() []string {
	return []string{"ask", "list"}
}
