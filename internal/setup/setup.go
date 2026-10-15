// Package setup stores an explicit profile name and test argv.
// It does not store a sandbox image. The daemon chooses the image.
package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/csy20/agt/internal/profile"
)

// File is the confirmed execution profile. Saving it does not start a run.
type File struct {
	Profile string   `json:"profile"`
	Argv    []string `json:"argv"`
	Workdir string   `json:"workdir"`
	Repo    string   `json:"repo,omitempty"`
}

// Path is the config file written after confirmation.
func Path(configDir string) string {
	return filepath.Join(configDir, "setup.json")
}

// Load reads a confirmed profile. A file that names an image is refused.
func Load(path string) (File, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return File{}, fmt.Errorf("setup file: %w", err)
	}
	if _, ok := raw["image"]; ok {
		return File{}, fmt.Errorf("setup file names an image; images stay under daemon control")
	}
	var file File
	if err := json.Unmarshal(body, &file); err != nil {
		return File{}, err
	}
	if err := file.check(); err != nil {
		return File{}, err
	}
	return file, nil
}

// Save writes the profile with mode 0600. It does not start a run.
func Save(path string, file File) error {
	if err := file.check(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (f File) check() error {
	req := profile.Request{
		Name: f.Profile, Argv: append([]string(nil), f.Argv...), Workdir: f.Workdir,
		TimeoutSec: 60, Memory: "256m", PIDs: 64,
	}
	if req.Workdir == "" {
		req.Workdir = "."
	}
	if err := profile.Validate(req); err != nil {
		return err
	}
	f.Profile = req.Name
	return nil
}

// ParseLine reads a confirmation line.
// "go -- go test ./..." sets the profile explicitly.
// Otherwise suggested is the profile and the line is the test argv.
func ParseLine(line, suggested string) (File, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return File{}, fmt.Errorf("a test command is required")
	}
	var name, rest string
	if profile, argv, ok := strings.Cut(line, " -- "); ok {
		name = strings.TrimSpace(profile)
		rest = strings.TrimSpace(argv)
	} else {
		name = strings.TrimSpace(suggested)
		rest = line
		if name == "" {
			return File{}, fmt.Errorf("name the profile, for example: go -- go test ./...")
		}
	}
	argv, err := splitArgs(rest)
	if err != nil {
		return File{}, err
	}
	file := File{Profile: name, Argv: argv, Workdir: "."}
	if err := file.check(); err != nil {
		return File{}, err
	}
	return file, nil
}

func splitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	quote := rune(0)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote in the test command")
	}
	flush()
	if len(out) == 0 {
		return nil, fmt.Errorf("a test command is required")
	}
	return out, nil
}
