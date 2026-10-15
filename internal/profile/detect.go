package profile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/csy20/agt/internal/tools"
)

// Suggestion is a discovery result. It does not authorize a run.
type Suggestion struct {
	Name   string
	Reason string
}

// Propose looks at project markers and returns one profile name, or none when
// the tree is ambiguous or unmarked. It does not read repository config as
// authority and does not follow secret directories.
func Propose(root string) Suggestion {
	var goMod, pkgJSON, python bool
	pythonWhy := ""
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name != "." && (tools.SkipDir(name) || tools.SecretDir(name)) {
				return filepath.SkipDir
			}
			if d.Type()&os.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		switch name {
		case "go.mod":
			goMod = true
		case "package.json":
			pkgJSON = true
		case "pyproject.toml", "requirements.txt", "setup.py":
			python = true
			pythonWhy = name
		default:
			if len(name) > 3 && name[len(name)-3:] == ".py" {
				python = true
				if pythonWhy == "" {
					pythonWhy = "a .py file"
				}
			}
		}
		return nil
	})
	if err != nil {
		return Suggestion{Reason: err.Error()}
	}
	matched := 0
	if goMod {
		matched++
	}
	if pkgJSON {
		matched++
	}
	if python {
		matched++
	}
	switch {
	case matched == 0:
		return Suggestion{Reason: "no Python, Go, or JavaScript project marker"}
	case matched > 1:
		return Suggestion{Reason: fmt.Sprintf("multiple languages match (%s); choose --profile explicitly", markers(goMod, pkgJSON, python))}
	case goMod:
		return Suggestion{Name: NameGo, Reason: "found go.mod"}
	case pkgJSON:
		return Suggestion{Name: NameJavaScript, Reason: "found package.json"}
	default:
		return Suggestion{Name: NamePython, Reason: "found " + pythonWhy}
	}
}

func markers(goMod, pkgJSON, python bool) string {
	var parts []string
	if goMod {
		parts = append(parts, "go.mod")
	}
	if pkgJSON {
		parts = append(parts, "package.json")
	}
	if python {
		parts = append(parts, "Python")
	}
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += ", "
		}
		out += part
	}
	return out
}
