package cli

import (
	"flag"
	"fmt"
	"os"
)

func lifecycleCmd(action string, args []string) int {
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true})); err != nil {
		return 2
	}
	rest := fs.Args()
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if action == "cleanup" {
		if len(rest) != 0 {
			fmt.Fprintln(os.Stderr, "usage: agt run cleanup")
			return 2
		}
		var out struct {
			Removed int    `json:"removed"`
			Error   string `json:"error"`
		}
		if err := postJSON(base+"/v1/cleanup", map[string]any{}, &out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("removed %d\n", out.Removed)
		return 0
	}
	if len(rest) != 1 {
		fmt.Fprintf(os.Stderr, "usage: agt run %s RUN\n", action)
		return 2
	}
	var view struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := postJSON(base+"/v1/runs/"+rest[0]+"/"+action, map[string]any{}, &view); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("%s %s\n", rest[0], view.State)
	return 0
}

func budgetResumeCmd(args []string) int {
	fs := flag.NewFlagSet("budget-resume", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	budget := fs.Int("budget-cents", -1, "new budget in cents; must not be lower than the current budget")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true, "budget-cents": true})); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: agt run budget-resume RUN [--budget-cents N]")
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	body := map[string]any{}
	if *budget >= 0 {
		body["budget_cents"] = *budget
	}
	var view struct {
		State       string `json:"state"`
		BudgetCents int    `json:"budget_cents"`
		Error       string `json:"error"`
	}
	if err := postJSON(base+"/v1/runs/"+rest[0]+"/budget-resume", body, &view); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("%s %s budget_cents %d\n", rest[0], view.State, view.BudgetCents)
	return 0
}
