package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/csy20/agt/internal/usage"
)

func listCmd(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	asJSON := fs.Bool("json", false, "print the JSON response")
	limit := fs.Int("limit", 20, "maximum runs, from 1 to 100")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true, "limit": true})); err != nil {
		return 2
	}
	if len(fs.Args()) != 0 || *limit < 1 || *limit > 100 {
		fmt.Fprintln(os.Stderr, "usage: agt run list [--limit N] [--json]")
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	raw, err := getBytes(fmt.Sprintf("%s/v1/runs?limit=%d", base, *limit), 4<<20)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		fmt.Println(strings.TrimSpace(string(raw)))
		return 0
	}
	text, err := formatRunList(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Print(text)
	return 0
}

func formatRunList(raw []byte) (string, error) {
	var body struct {
		Runs []struct {
			ID          string `json:"id"`
			State       string `json:"state"`
			Goal        string `json:"goal"`
			SpentMicros int64  `json:"spent_micros"`
			RepoPath    string `json:"repo_path"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	if len(body.Runs) == 0 {
		return "no runs\nnext: agt fix --repo PATH --profile python --test-arg python --test-arg -m --test-arg unittest --test-arg tests.test_retry \"goal\"\n", nil
	}
	var b strings.Builder
	for _, run := range body.Runs {
		goal := strings.ReplaceAll(strings.TrimSpace(run.Goal), "\n", " ")
		if len(goal) > 80 {
			goal = goal[:80] + "..."
		}
		fmt.Fprintf(&b, "%s  %s  %s  %s\n", run.ID, run.State, usage.USD(run.SpentMicros), goal)
		if run.RepoPath != "" {
			fmt.Fprintf(&b, "  repo %s\n", run.RepoPath)
		}
	}
	b.WriteString("next: agt run status RUN\n")
	b.WriteString("next: agt run attach RUN\n")
	b.WriteString("next: agt run diff RUN\n")
	b.WriteString("next: agt run export RUN --output agt-patch.json\n")
	b.WriteString("next: agt run apply RUN --digest DIGEST\n")
	return b.String(), nil
}

func rawMessage(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

func statusCmd(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	asJSON := fs.Bool("json", false, "print the JSON response")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true})); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: agt run status RUN [--json]")
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		raw, err := getBytes(base+"/v1/runs/"+rest[0], 4<<20)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(strings.TrimSpace(string(raw)))
		return 0
	}
	return printStatus(base, rest[0])
}

func printStatus(base, runID string) int {
	req, err := http.NewRequest(http.MethodGet, base+"/v1/runs/"+runID, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := setAuth(req); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	var view map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %v\n", resp.StatusCode, view["error"])
		return 1
	}
	fmt.Printf("state %v\n", view["state"])
	if task, _ := view["task_type"].(string); task != "" {
		fmt.Printf("task %s\n", task)
	}
	if outcome, _ := view["verification_outcome"].(string); outcome != "" {
		fmt.Printf("verification %s\n", outcome)
	}
	if digest, _ := view["contract_digest"].(string); digest != "" {
		fmt.Printf("contract %s\n", digest)
	}
	for _, line := range contractLines(rawMessage(view["contract"])) {
		fmt.Println(line)
	}
	failures := failureLines(rawMessage(view["final"]))
	if len(failures) == 0 {
		failures = failureLines(rawMessage(view["baseline"]))
	}
	for _, line := range failures {
		fmt.Println("failure " + line)
	}
	if n, ok := asInt64(view["usage_micros"]); ok {
		fmt.Printf("usage %s\n", usage.USD(n))
	} else if n, ok := asInt64(view["spent_micros"]); ok {
		fmt.Printf("usage %s\n", usage.USD(n))
	}
	if repo, _ := view["repo_path"].(string); repo != "" {
		fmt.Printf("repo %s\n", repo)
	}
	if work, _ := view["worktree"].(string); work != "" {
		fmt.Printf("worktree %s\n", work)
	}
	state, _ := view["state"].(string)
	for _, line := range nextCommands(runID, state, "") {
		fmt.Printf("next: %s\n", line)
	}
	raw, _ := json.MarshalIndent(view, "", "  ")
	fmt.Println(string(raw))
	return 0
}
