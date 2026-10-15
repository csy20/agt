// Package patch builds and applies a patch.v1 artifact.
//
// The artifact is the machine-applicable record of a run. It stores exact
// file bytes, modes, and hashes. It does not depend on the truncated unified
// diff shown in the terminal. The supervisor does not apply it. A local apply
// is an explicit decision bound to the run, the recorded repository, the
// snapshot, and this digest.
package patch

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/csy20/agt/internal/tools"
)

const (
	// Contract is the artifact schema.
	Contract = "patch.v1"
	// Strategy names the workspace choice: a filesystem snapshot plus this
	// artifact, not a Git worktree. The snapshot includes uncommitted files.
	Strategy = "snapshot-plus-patch"

	OpAdd    = "add"
	OpModify = "modify"
	OpDelete = "delete"
)

// Meta binds an artifact to one run and the repository it may update.
type Meta struct {
	RunID    string
	RepoPath string
	Revision string
}

// File is one added, modified, or deleted regular file.
// ContentBase64 is the exact after-bytes for add and modify. Line endings
// are not normalized. Delete omits content.
type File struct {
	Op            string `json:"op"`
	Path          string `json:"path"`
	BeforeMode    string `json:"before_mode"`
	AfterMode     string `json:"after_mode"`
	BeforeSHA256  string `json:"before_sha256"`
	AfterSHA256   string `json:"after_sha256"`
	ContentBase64 string `json:"content_base64,omitempty"`
}

// Artifact is a complete patch. Digest covers the identity and the file
// records. Revision is provenance from the source HEAD when one exists; it
// is not part of the digest, because the snapshot is the working tree rather
// than a commit.
type Artifact struct {
	Contract   string `json:"contract"`
	Strategy   string `json:"strategy"`
	RunID      string `json:"run_id"`
	RepoPath   string `json:"repo_path"`
	Revision   string `json:"revision"`
	BaseDigest string `json:"base_digest"`
	Digest     string `json:"digest"`
	Files      []File `json:"files"`
}

// Build compares the immutable base snapshot with the run worktree.
func Build(base, work string, meta Meta) (Artifact, error) {
	baseInv, err := inventory(base)
	if err != nil {
		return Artifact{}, err
	}
	workInv, err := inventory(work)
	if err != nil {
		return Artifact{}, err
	}
	keys := make([]string, 0, len(baseInv)+len(workInv))
	seen := map[string]struct{}{}
	for _, inv := range []map[string]fileInfo{baseInv, workInv} {
		for path := range inv {
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			keys = append(keys, path)
		}
	}
	sort.Strings(keys)
	files := make([]File, 0)
	for _, path := range keys {
		before, hasBefore := baseInv[path]
		after, hasAfter := workInv[path]
		switch {
		case hasAfter && !hasBefore:
			files = append(files, fileRecord(OpAdd, path, fileInfo{}, after))
		case hasBefore && !hasAfter:
			files = append(files, fileRecord(OpDelete, path, before, fileInfo{}))
		case before.sum != after.sum || before.mode != after.mode:
			files = append(files, fileRecord(OpModify, path, before, after))
		}
	}
	art := Artifact{
		Contract:   Contract,
		Strategy:   Strategy,
		RunID:      meta.RunID,
		RepoPath:   meta.RepoPath,
		Revision:   meta.Revision,
		BaseDigest: digestBase(baseInv),
		Files:      files,
	}
	art.Digest = Digest(art)
	return art, nil
}

// Digest is the sha256 of the canonical artifact records. It ignores
// revision and the base64 payload. The payload is bound by AfterSHA256.
func Digest(art Artifact) string {
	files := append([]File(nil), art.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n%s\n%s\n", Contract, art.RunID, art.RepoPath, art.BaseDigest)
	for _, f := range files {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\n", f.Op, f.Path, f.BeforeMode, f.AfterMode, f.BeforeSHA256, f.AfterSHA256)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func fileRecord(op, path string, before, after fileInfo) File {
	f := File{
		Op:           op,
		Path:         path,
		BeforeMode:   before.mode,
		AfterMode:    after.mode,
		BeforeSHA256: before.sum,
		AfterSHA256:  after.sum,
	}
	if op != OpDelete {
		f.ContentBase64 = base64.StdEncoding.EncodeToString(after.data)
	}
	return f
}

type fileInfo struct {
	mode string
	sum  string
	data []byte
}

func inventory(root string) (map[string]fileInfo, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	out := map[string]fileInfo{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to include symlink %s", rel)
		}
		if d.IsDir() {
			if tools.SkipDir(d.Name()) || tools.SecretDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || tools.SecretFile(d.Name()) {
			return nil
		}
		safe, err := tools.SafeRel(rel)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		slash := filepath.ToSlash(safe)
		st, err := d.Info()
		if err != nil {
			return err
		}
		if st.Size() > toolsMaxFile {
			return fmt.Errorf("file %s is %d bytes, over the %d byte patch limit", slash, st.Size(), toolsMaxFile)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[slash] = fileInfo{mode: formatMode(st.Mode()), sum: hashBytes(body), data: body}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func digestBase(inv map[string]fileInfo) string {
	keys := make([]string, 0, len(inv))
	for path := range inv {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, path := range keys {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", inv[path].mode, path, inv[path].sum)
	}
	return hashBytes([]byte(b.String()))
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func formatMode(m os.FileMode) string {
	return fmt.Sprintf("%04o", m.Perm())
}

// toolsMaxFile matches the workspace copy limit without importing workspace.
const toolsMaxFile = 8 << 20
