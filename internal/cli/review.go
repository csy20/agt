package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func diffCmd(args []string) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true})); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: agt run diff RUN")
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var view struct {
		Diff     string `json:"diff"`
		Digest   string `json:"digest"`
		Complete bool   `json:"complete"`
		Note     string `json:"note"`
		RepoPath string `json:"repo_path"`
	}
	if err := getJSON(base+"/v1/runs/"+rest[0]+"/diff", &view); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if view.Diff != "" {
		fmt.Print(view.Diff)
		if !strings.HasSuffix(view.Diff, "\n") {
			fmt.Println()
		}
	}
	if !view.Complete && view.Note != "" {
		fmt.Fprintln(os.Stderr, view.Note)
	}
	fmt.Printf("digest %s\nrepo %s\n", view.Digest, view.RepoPath)
	return 0
}

func exportCmd(args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	output := fs.String("output", "", "patch artifact path")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true, "output": true})); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 || *output == "" {
		fmt.Fprintln(os.Stderr, "usage: agt run export RUN --output FILE")
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	body, err := getBytes(base+"/v1/runs/"+rest[0]+"/patch", 64<<20)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var art struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(body, &art); err != nil || art.Digest == "" {
		fmt.Fprintln(os.Stderr, "patch response has no digest")
		return 1
	}
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := file.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("wrote %s\ndigest %s\n", *output, art.Digest)
	return 0
}

func applyCmd(args []string) int {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	digest := fs.String("digest", "", "digest of the artifact to apply")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true, "digest": true})); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: agt run apply RUN --digest DIGEST")
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	body, err := getBytes(base+"/v1/runs/"+rest[0]+"/patch", 64<<20)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var art struct {
		Digest   string `json:"digest"`
		RepoPath string `json:"repo_path"`
		Files    []struct {
			Op           string `json:"op"`
			Path         string `json:"path"`
			BeforeMode   string `json:"before_mode"`
			AfterMode    string `json:"after_mode"`
			BeforeSHA256 string `json:"before_sha256"`
			AfterSHA256  string `json:"after_sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &art); err != nil || art.Digest == "" {
		fmt.Fprintln(os.Stderr, "patch response has no digest")
		return 1
	}
	var preview struct {
		Diff     string `json:"diff"`
		Complete bool   `json:"complete"`
		Note     string `json:"note"`
	}
	if err := getJSON(base+"/v1/runs/"+rest[0]+"/diff", &preview); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if preview.Diff != "" {
		fmt.Print(preview.Diff)
		if !strings.HasSuffix(preview.Diff, "\n") {
			fmt.Println()
		}
	}
	if !preview.Complete && preview.Note != "" {
		fmt.Fprintln(os.Stderr, preview.Note)
	}
	for _, f := range art.Files {
		fmt.Printf("%s %s %s -> %s %s -> %s\n", f.Op, f.Path, f.BeforeMode, f.AfterMode, shortHash(f.BeforeSHA256), shortHash(f.AfterSHA256))
	}
	fmt.Printf("digest %s\nrepo %s\nfiles %d\n", art.Digest, art.RepoPath, len(art.Files))
	if *digest == "" {
		fmt.Fprintf(os.Stderr, "pass --digest %s to apply this artifact\n", art.Digest)
		return 2
	}
	if *digest != art.Digest {
		fmt.Fprintln(os.Stderr, "digest does not match the current artifact")
		return 2
	}
	var applied struct {
		Applied int    `json:"applied"`
		Error   string `json:"error"`
	}
	if err := postJSON(base+"/v1/runs/"+rest[0]+"/apply", map[string]any{"digest": *digest}, &applied); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if applied.Error != "" {
		fmt.Fprintln(os.Stderr, applied.Error)
		return 1
	}
	fmt.Printf("applied %d\n", applied.Applied)
	return 0
}

func shortHash(s string) string {
	if s == "" {
		return "-"
	}
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func getBytes(url string, limit int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if err := setAuth(req); err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}
