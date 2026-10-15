package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/csy20/agt/internal/client"
	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/workspace"
)

func sessionCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: agt session list|new|open|show|submit")
		return 2
	}
	switch args[0] {
	case "list":
		return sessionList(args[1:])
	case "new":
		return sessionNew(args[1:])
	case "open":
		return sessionOpen(args[1:])
	case "show":
		return sessionShow(args[1:])
	case "submit":
		return sessionSubmit(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown session command %q\n", args[0])
		return 2
	}
}

func sessionList(args []string) int {
	fs := flag.NewFlagSet("session list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	repoFlag := fs.String("repo", "", "repository path")
	asJSON := fs.Bool("json", false, "print the JSON response")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true, "repo": true})); err != nil {
		return 2
	}
	repo, err := sessionRepo(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var body struct {
		Sessions []client.Session `json:"sessions"`
		Error    string           `json:"error"`
	}
	if err := getJSON(fmt.Sprintf("%s/v1/sessions?repo_path=%s", base, url.QueryEscape(repo)), &body); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		raw, _ := json.Marshal(body)
		fmt.Println(string(raw))
		return 0
	}
	if len(body.Sessions) == 0 {
		fmt.Printf("no sessions for %s\nnext: agt session new --repo %s\n", repo, repo)
		return 0
	}
	for _, sess := range body.Sessions {
		title := sess.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Printf("%s  %s\n  repo %s\n", sess.ID, title, sess.RepoPath)
	}
	fmt.Println("next: agt session open SESSION")
	return 0
}

func sessionNew(args []string) int {
	fs := flag.NewFlagSet("session new", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	repoFlag := fs.String("repo", "", "repository path")
	title := fs.String("title", "", "optional title")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true, "repo": true, "title": true})); err != nil {
		return 2
	}
	repo, err := sessionRepo(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var created struct {
		Session client.Session `json:"session"`
		Error   string         `json:"error"`
	}
	if err := postJSON(base+"/v1/sessions", map[string]any{"repo_path": repo, "title": *title}, &created); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if created.Error != "" {
		fmt.Fprintln(os.Stderr, created.Error)
		return 1
	}
	fmt.Printf("session %s\nrepo %s\n", created.Session.ID, created.Session.RepoPath)
	fmt.Println("No run was started. Patches were not applied.")
	return 0
}

func sessionOpen(args []string) int {
	fs := flag.NewFlagSet("session open", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	repoFlag := fs.String("repo", "", "repository path")
	asJSON := fs.Bool("json", false, "print the JSON response")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true, "repo": true})); err != nil {
		return 2
	}
	if len(fs.Args()) != 1 {
		fmt.Fprintln(os.Stderr, "usage: agt session open SESSION [--repo PATH]")
		return 2
	}
	repo, err := sessionRepo(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var view client.SessionView
	if err := postJSON(base+"/v1/sessions/"+fs.Arg(0)+"/open", map[string]any{"repo_path": repo}, &view); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON && view.ActiveRunID == "" {
		raw, _ := json.Marshal(view)
		fmt.Println(string(raw))
		fmt.Fprintln(os.Stderr, "No run was started. Patches were not applied.")
		return 0
	}
	if view.ActiveRunID != "" {
		fmt.Printf("session %s\nactive run %s\n", view.Session.ID, view.ActiveRunID)
		fmt.Println("attaching. This does not start a run or apply a patch.")
		return attach(base, view.ActiveRunID, *asJSON)
	}
	fmt.Print(formatSession(view))
	fmt.Fprintln(os.Stderr, "No run was started. Patches were not applied.")
	return 0
}

func sessionShow(args []string) int {
	fs := flag.NewFlagSet("session show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	repoFlag := fs.String("repo", "", "repository path")
	asJSON := fs.Bool("json", false, "print the JSON response")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true, "repo": true})); err != nil {
		return 2
	}
	if len(fs.Args()) != 1 {
		fmt.Fprintln(os.Stderr, "usage: agt session show SESSION [--repo PATH]")
		return 2
	}
	repo, err := sessionRepo(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var view client.SessionView
	if err := getJSON(fmt.Sprintf("%s/v1/sessions/%s?repo_path=%s", base, fs.Arg(0), url.QueryEscape(repo)), &view); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		raw, _ := json.Marshal(view)
		fmt.Println(string(raw))
		return 0
	}
	fmt.Print(formatSession(view))
	fmt.Fprintln(os.Stderr, "No run was started. Patches were not applied.")
	return 0
}

func sessionSubmit(args []string) int {
	fs := flag.NewFlagSet("session submit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	repoFlag := fs.String("repo", "", "repository path")
	goal := fs.String("goal", "", "prompt to store")
	messageID := fs.String("message-id", "", "stable client message id")
	providerFlag := fs.String("provider", "", "fake or openai")
	scriptFlag := fs.String("script", "", "fake-provider script")
	modelFlag := fs.String("model", "", "require this AGT_MODEL_NAME on agtd")
	interaction := fs.String("interaction", "agent", "agent, ask, or plan")
	pf := addProfileFlags(fs)
	flags := runValueFlags(false)
	flags["message-id"] = true
	flags["interaction"] = true
	if err := fs.Parse(reorderFlags(args, flags)); err != nil {
		return 2
	}
	if len(fs.Args()) != 1 || strings.TrimSpace(*goal) == "" {
		fmt.Fprintln(os.Stderr, "usage: agt session submit SESSION --goal TEXT --repo PATH --profile NAME --message-id ID")
		return 2
	}
	repo, err := sessionRepo(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	prof, err := pf.request()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	provider, _, script, err := runInputs(*providerFlag, repo, *scriptFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := matchModel(base, *modelFlag); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	msgID := *messageID
	if msgID == "" {
		msgID = id.New("msg_")
	}
	fmt.Fprintf(os.Stderr, "client_message_id %s\n", msgID)
	body := map[string]any{
		"goal": *goal, "content": *goal, "repo_path": repo, "provider": provider,
		"interaction": *interaction, "profile": prof, "client_message_id": msgID,
		"budget_cents": 100,
	}
	if script != "" {
		body["script_path"] = script
	}
	var sub client.Submission
	if err := postJSON(base+"/v1/sessions/"+fs.Arg(0)+"/messages", body, &sub); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if sub.Error != "" && sub.RunID == "" {
		fmt.Fprintln(os.Stderr, sub.Error)
		return 1
	}
	if !sub.Created {
		fmt.Printf("session %s\nexisting run %s\n", sub.SessionID, sub.RunID)
		fmt.Println("The same client message id did not create another run.")
		return 0
	}
	if sub.Notice != "" {
		fmt.Fprintln(os.Stderr, sub.Notice)
	}
	for _, patch := range sub.Pending {
		fmt.Fprintf(os.Stderr, "pending %s change=%s digest=%s\n", patch.RunID, patch.Change, patch.Digest)
		fmt.Fprintln(os.Stderr, "Its edits are absent from the new snapshot. It was not applied and it was not discarded.")
	}
	fmt.Printf("run %s\n", sub.RunID)
	return attach(base, sub.RunID, false)
}

func formatSession(view client.SessionView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "session %s\nrepo %s\n", view.Session.ID, view.Session.RepoPath)
	if view.Session.Title != "" {
		fmt.Fprintf(&b, "title %s\n", view.Session.Title)
	}
	if len(view.Messages) == 0 {
		b.WriteString("no messages\n")
	}
	for _, msg := range view.Messages {
		fmt.Fprintf(&b, "%d  %s  %s", msg.Seq, msg.Role, msg.Kind)
		if msg.RunID != "" {
			fmt.Fprintf(&b, "  %s", msg.RunID)
		}
		b.WriteByte('\n')
		b.WriteString(msg.Content)
		if !strings.HasSuffix(msg.Content, "\n") {
			b.WriteByte('\n')
		}
	}
	if len(view.Pending) == 0 {
		b.WriteString("no pending patches\n")
	}
	for _, patch := range view.Pending {
		fmt.Fprintf(&b, "pending %s change=%s\n", patch.RunID, patch.Change)
		if patch.Digest != "" {
			fmt.Fprintf(&b, "digest %s\n", patch.Digest)
		}
		b.WriteString("Its edits are absent from a new snapshot. It was not applied and it was not discarded.\n")
	}
	if view.ActiveRunID != "" {
		fmt.Fprintf(&b, "active run %s\n", view.ActiveRunID)
	}
	return b.String()
}

func sessionRepo(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = wd
	}
	repo, err := workspace.CanonicalRepo(path)
	if err != nil {
		return "", err
	}
	return repo, nil
}

func sessionCommands() []string {
	return []string{"list", "new", "open", "show", "submit"}
}
