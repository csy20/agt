package tools

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxDiffBytes = 1 << 20
	maxDiffLines = 2500
	diffContext  = 3
)

// Diff returns a unified diff of work against the immutable base snapshot.
// It does not invoke a host diff binary and does not open /dev/null.
// A failure to read a file is an error. Identical trees return "(no changes)".
func Diff(base, work string) (string, error) {
	files, err := unionFiles(base, work)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, rel := range files {
		chunk, err := diffRel(base, work, rel)
		if err != nil {
			return "", err
		}
		b.WriteString(chunk)
		if b.Len() > maxDiffBytes {
			return "", fmt.Errorf("diff exceeds %d bytes", maxDiffBytes)
		}
	}
	if b.Len() == 0 {
		return "(no changes)\n", nil
	}
	return b.String(), nil
}

func unionFiles(base, work string) ([]string, error) {
	seen := map[string]struct{}{}
	for _, root := range []string{base, work} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() != "." && SkipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			seen[rel] = struct{}{}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(seen))
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

func diffRel(base, work, rel string) (string, error) {
	label := repoLabel(rel)
	oldData, oldExists, err := readDiffSide(filepath.Join(base, rel), label)
	if err != nil {
		return "", err
	}
	newData, newExists, err := readDiffSide(filepath.Join(work, rel), label)
	if err != nil {
		return "", err
	}
	if oldExists && newExists && bytes.Equal(oldData, newData) {
		return "", nil
	}
	oldLabel := "a/" + label
	newLabel := "b/" + label
	if !oldExists {
		oldLabel = "/dev/null"
	}
	if !newExists {
		newLabel = "/dev/null"
	}
	if isBinary(oldData) || isBinary(newData) {
		return fmt.Sprintf("Binary files %s and %s differ\n", oldLabel, newLabel), nil
	}
	oldText := splitDiff(oldData)
	newText := splitDiff(newData)
	if len(oldText.lines) > maxDiffLines || len(newText.lines) > maxDiffLines {
		return "", fmt.Errorf("diff: %s has %d/%d lines, over the %d line limit", label, len(oldText.lines), len(newText.lines), maxDiffLines)
	}
	return unified(oldLabel, newLabel, oldText, newText), nil
}

func readDiffSide(path, label string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("diff: read %s: %w", label, err)
	}
	return data, true, nil
}

func isBinary(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0
}

type diffText struct {
	lines []string
	noNL  bool
}

func splitDiff(data []byte) diffText {
	if len(data) == 0 {
		return diffText{}
	}
	noNL := data[len(data)-1] != '\n'
	s := strings.TrimSuffix(string(data), "\n")
	return diffText{lines: strings.Split(s, "\n"), noNL: noNL}
}

type lineEdit struct {
	kind  byte
	text  string
	aLine int
	bLine int
}

func unified(oldLabel, newLabel string, oldText, newText diffText) string {
	edits := align(oldText.lines, newText.lines)
	if len(edits) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", oldLabel, newLabel)
	for _, h := range groupHunks(edits, diffContext) {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.aStart, h.aCount, h.bStart, h.bCount)
		for _, line := range h.lines {
			b.WriteByte(line.kind)
			b.WriteString(line.text)
			b.WriteByte('\n')
			if lineNeedsNoNL(line, oldText, newText) {
				b.WriteString("\\ No newline at end of file\n")
			}
		}
	}
	return b.String()
}

func lineNeedsNoNL(line lineEdit, oldText, newText diffText) bool {
	switch line.kind {
	case ' ':
		return oldText.noNL && line.aLine == len(oldText.lines) && len(oldText.lines) > 0
	case '-':
		return oldText.noNL && line.aLine == len(oldText.lines) && len(oldText.lines) > 0
	case '+':
		return newText.noNL && line.bLine == len(newText.lines) && len(newText.lines) > 0
	default:
		return false
	}
}

type diffHunk struct {
	aStart int
	aCount int
	bStart int
	bCount int
	lines  []lineEdit
}

func groupHunks(edits []lineEdit, context int) []diffHunk {
	var changes []int
	for i, edit := range edits {
		if edit.kind != ' ' {
			changes = append(changes, i)
		}
	}
	if len(changes) == 0 {
		return nil
	}
	type span struct{ start, end int }
	var groups []span
	start, end := changes[0], changes[0]
	for _, idx := range changes[1:] {
		if idx-end-1 <= 2*context {
			end = idx
			continue
		}
		groups = append(groups, span{start, end})
		start, end = idx, idx
	}
	groups = append(groups, span{start, end})

	hunks := make([]diffHunk, 0, len(groups))
	for _, g := range groups {
		from := g.start - context
		if from < 0 {
			from = 0
		}
		to := g.end + context
		if to >= len(edits) {
			to = len(edits) - 1
		}
		lines := edits[from : to+1]
		h := diffHunk{lines: lines}
		for _, line := range lines {
			switch line.kind {
			case ' ', '-':
				if h.aCount == 0 {
					h.aStart = line.aLine
				}
				h.aCount++
			}
			switch line.kind {
			case ' ', '+':
				if h.bCount == 0 {
					h.bStart = line.bLine
				}
				h.bCount++
			}
		}
		hunks = append(hunks, h)
	}
	return hunks
}

func align(a, b []string) []lineEdit {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	max := n + m
	offset := max
	v := make([]int, 2*max+1)
	trace := make([][]int, 0, max+1)
	for d := 0; d <= max; d++ {
		trace = append(trace, append([]int(nil), v...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				return backtrack(a, b, trace, offset)
			}
		}
	}
	return backtrack(a, b, trace, offset)
}

func backtrack(a, b []string, trace [][]int, offset int) []lineEdit {
	x, y := len(a), len(b)
	rev := make([]lineEdit, 0, x+y)
	for d := len(trace) - 1; d >= 0; d-- {
		v := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[offset+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			rev = append(rev, lineEdit{kind: ' ', text: a[x-1], aLine: x, bLine: y})
			x--
			y--
		}
		if d == 0 {
			break
		}
		if x == prevX {
			rev = append(rev, lineEdit{kind: '+', text: b[y-1], bLine: y})
			y--
		} else {
			rev = append(rev, lineEdit{kind: '-', text: a[x-1], aLine: x})
			x--
		}
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}
