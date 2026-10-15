package profile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/csy20/agt/internal/tools"
)

// Contract is the execution-profile version recorded with a run.
const Contract = "profile.v1"

const (
	NamePython     = "python"
	NameGo         = "go"
	NameJavaScript = "javascript"
	NameTypeScript = "typescript"
)

// Request is the user-selected profile. It has no image, environment, or
// network fields. The daemon fills those. Repository files cannot.
type Request struct {
	Name       string   `json:"name"`
	Argv       []string `json:"argv"`
	Workdir    string   `json:"workdir,omitempty"`
	TimeoutSec int      `json:"timeout_seconds,omitempty"`
	Memory     string   `json:"memory,omitempty"`
	PIDs       int      `json:"pids,omitempty"`
	Prepare    bool     `json:"prepare,omitempty"`
}

// Profile is the validated execution contract stored with a run.
type Profile struct {
	Contract   string   `json:"contract"`
	Name       string   `json:"name"`
	Alias      string   `json:"alias,omitempty"`
	Image      string   `json:"image"`
	Arch       string   `json:"arch"`
	Argv       []string `json:"argv"`
	Workdir    string   `json:"workdir"`
	TimeoutSec int      `json:"timeout_seconds"`
	Memory     string   `json:"memory"`
	CPUs       string   `json:"cpus"`
	PIDs       int      `json:"pids"`
	Env        []string `json:"env"`
	Network    string   `json:"network"`
	Deps       string   `json:"deps"`
	Prepare    []string `json:"prepare"`
}

// Identity is the profile record exposed on the run. It omits the environment
// allowlist, which is fixed by the profile name.
type Identity struct {
	Contract   string   `json:"contract"`
	Name       string   `json:"name"`
	Alias      string   `json:"alias,omitempty"`
	Image      string   `json:"image"`
	Arch       string   `json:"arch"`
	Argv       []string `json:"argv"`
	Workdir    string   `json:"workdir"`
	TimeoutSec int      `json:"timeout_seconds"`
	Memory     string   `json:"memory"`
	CPUs       string   `json:"cpus"`
	PIDs       int      `json:"pids"`
	Network    string   `json:"network"`
	Deps       string   `json:"deps"`
}

// Images are the daemon-configured runtime tags. The client does not send them.
type Images struct {
	Python string
	Go     string
	Node   string
}

func (m Images) For(name string) (string, error) {
	var image string
	switch name {
	case NamePython:
		image = m.Python
	case NameGo:
		image = m.Go
	case NameJavaScript:
		image = m.Node
	default:
		return "", fmt.Errorf("unknown profile %q", name)
	}
	if strings.TrimSpace(image) == "" {
		return "", fmt.Errorf("no image configured for profile %s", name)
	}
	return image, nil
}

// List returns the configured image tags in a stable order.
func (m Images) List() []string {
	var out []string
	for _, image := range []string{m.Python, m.Go, m.Node} {
		if strings.TrimSpace(image) != "" {
			out = append(out, image)
		}
	}
	return out
}

// Resolve fills the daemon-owned fields and returns a profile that passes Check.
// goarch is runtime.GOARCH. The container architecture is recorded as linux/<goarch>.
func Resolve(req Request, images Images, goarch string) (Profile, error) {
	if err := validateRequest(req); err != nil {
		return Profile{}, err
	}
	name, alias, err := canonical(req.Name)
	if err != nil {
		return Profile{}, err
	}
	image, err := images.For(name)
	if err != nil {
		return Profile{}, err
	}
	if err := cleanImage(image); err != nil {
		return Profile{}, err
	}
	arch, err := linuxArch(goarch)
	if err != nil {
		return Profile{}, err
	}
	workdir, err := cleanWorkdir(req.Workdir)
	if err != nil {
		return Profile{}, err
	}
	timeout := req.TimeoutSec
	if timeout == 0 {
		timeout = 60
	}
	memory := req.Memory
	if memory == "" {
		memory = "256m"
	}
	pids := req.PIDs
	if pids == 0 {
		pids = 64
	}
	p := Profile{
		Contract:   Contract,
		Name:       name,
		Alias:      alias,
		Image:      image,
		Arch:       arch,
		Argv:       append([]string(nil), req.Argv...),
		Workdir:    workdir,
		TimeoutSec: timeout,
		Memory:     memory,
		CPUs:       "1",
		PIDs:       pids,
		Env:        append([]string(nil), EnvFor(name)...),
		Network:    "none",
		Deps:       "prebuilt",
		Prepare:    []string{},
	}
	if err := p.Check(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Parse reads a stored profile and rejects an empty object. A run without a
// profile does not fall back to a built-in test command.
func Parse(raw json.RawMessage) (Profile, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" || string(bytes.TrimSpace(raw)) == "{}" {
		return Profile{}, fmt.Errorf("run has no execution profile")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p Profile
	if err := dec.Decode(&p); err != nil {
		return Profile{}, err
	}
	if err := p.Check(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

func (p Profile) Canonical() ([]byte, error) {
	if p.Prepare == nil {
		p.Prepare = []string{}
	}
	return json.Marshal(p)
}

func (p Profile) Identity() Identity {
	return Identity{
		Contract: p.Contract, Name: p.Name, Alias: p.Alias, Image: p.Image, Arch: p.Arch,
		Argv: append([]string(nil), p.Argv...), Workdir: p.Workdir, TimeoutSec: p.TimeoutSec,
		Memory: p.Memory, CPUs: p.CPUs, PIDs: p.PIDs, Network: p.Network, Deps: p.Deps,
	}
}

// ContainerArgv prefixes the user argv with a kill timeout. It is not a shell.
func (p Profile) ContainerArgv() []string {
	out := make([]string, 0, len(p.Argv)+4)
	out = append(out, "timeout", "-s", "KILL", strconv.Itoa(p.TimeoutSec))
	out = append(out, p.Argv...)
	return out
}

// ContainerWorkdir is the docker exec working directory under /workspace.
func (p Profile) ContainerWorkdir() string {
	if p.Workdir == "" || p.Workdir == "." {
		return "/workspace"
	}
	return "/workspace/" + p.Workdir
}

// HostDir maps the profile workdir onto a copied worktree for the test-only host runner.
func (p Profile) HostDir(root string) string {
	if p.Workdir == "" || p.Workdir == "." {
		return root
	}
	return filepath.Join(root, filepath.FromSlash(p.Workdir))
}

// HostEnv is the profile allowlist plus the host PATH needed to find the runtime.
// Container paths are rewritten. Host secrets are not copied.
func HostEnv(p Profile, root string) []string {
	env := make([]string, 0, len(p.Env)+4)
	if path := os.Getenv("PATH"); path != "" {
		env = append(env, "PATH="+path)
	}
	for _, key := range []string{"PATHEXT", "SYSTEMROOT", "SystemRoot", "WINDIR"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	for _, item := range p.Env {
		key, val, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if key == "PYTHONPATH" {
			val = root
		}
		env = append(env, key+"="+val)
	}
	return env
}

// Check rejects a profile whose daemon-owned fields were changed after Resolve.
func (p Profile) Check() error {
	if p.Contract != Contract {
		return fmt.Errorf("profile contract must be %s", Contract)
	}
	if p.Name != NamePython && p.Name != NameGo && p.Name != NameJavaScript {
		return fmt.Errorf("unknown profile %q", p.Name)
	}
	if err := aliasOK(p.Name, p.Alias); err != nil {
		return err
	}
	if err := cleanImage(p.Image); err != nil {
		return err
	}
	if _, err := linuxArch(strings.TrimPrefix(p.Arch, "linux/")); err != nil || !strings.HasPrefix(p.Arch, "linux/") {
		return fmt.Errorf("profile arch must be linux/<arch>")
	}
	if err := validateArgv(p.Name, p.Argv); err != nil {
		return err
	}
	if p.Workdir != "." {
		if _, err := tools.SafeRel(p.Workdir); err != nil {
			return fmt.Errorf("workdir: %w", err)
		}
		if filepath.ToSlash(p.Workdir) != p.Workdir {
			return fmt.Errorf("workdir must use forward slashes")
		}
	}
	if p.TimeoutSec < 1 || p.TimeoutSec > 300 {
		return fmt.Errorf("timeout must be 1..300 seconds")
	}
	if !allowedMemory(p.Memory) {
		return fmt.Errorf("memory must be 64m, 128m, 256m, or 512m")
	}
	if p.CPUs != "1" {
		return fmt.Errorf("cpus must be 1")
	}
	if p.PIDs < 32 || p.PIDs > 256 {
		return fmt.Errorf("pids must be 32..256")
	}
	if strings.Join(p.Env, "\n") != strings.Join(EnvFor(p.Name), "\n") {
		return fmt.Errorf("profile environment does not match the %s allowlist", p.Name)
	}
	if p.Network != "none" {
		return fmt.Errorf("network must be none")
	}
	if p.Deps != "prebuilt" {
		return fmt.Errorf("deps must be prebuilt")
	}
	if len(p.Prepare) != 0 {
		return fmt.Errorf("these profiles have no prepare step; dependencies are prebuilt and the network stays disabled")
	}
	return nil
}

// EnvFor is the container environment for a canonical profile name.
// Values are fixed. They are not taken from the host or the repository.
func EnvFor(name string) []string {
	switch name {
	case NamePython:
		return []string{
			"HOME=/tmp",
			"PYTHONDONTWRITEBYTECODE=1",
			"PYTHONUNBUFFERED=1",
			"PYTHONPATH=/workspace",
		}
	case NameGo:
		return []string{
			"HOME=/tmp",
			"TMPDIR=/tmp",
			"GOCACHE=/tmp/gocache",
			"GOPATH=/tmp/gopath",
			"GO111MODULE=on",
			"GOPROXY=off",
			"GOSUMDB=off",
			"GOTOOLCHAIN=local",
			"CGO_ENABLED=0",
		}
	case NameJavaScript:
		return []string{
			"HOME=/tmp",
			"NODE_NO_WARNINGS=1",
		}
	default:
		return nil
	}
}

// Validate checks a user request before the daemon adds an image.
func Validate(req Request) error {
	return validateRequest(req)
}

func validateRequest(req Request) error {
	if req.Prepare {
		return fmt.Errorf("these profiles have no prepare step; dependencies are prebuilt and the network stays disabled")
	}
	name, _, err := canonical(req.Name)
	if err != nil {
		return err
	}
	if err := validateArgv(name, req.Argv); err != nil {
		return err
	}
	if _, err := cleanWorkdir(req.Workdir); err != nil {
		return err
	}
	if req.TimeoutSec != 0 && (req.TimeoutSec < 1 || req.TimeoutSec > 300) {
		return fmt.Errorf("timeout must be 1..300 seconds")
	}
	if req.Memory != "" && !allowedMemory(req.Memory) {
		return fmt.Errorf("memory must be 64m, 128m, 256m, or 512m")
	}
	if req.PIDs != 0 && (req.PIDs < 32 || req.PIDs > 256) {
		return fmt.Errorf("pids must be 32..256")
	}
	return nil
}

func canonical(name string) (string, string, error) {
	switch name {
	case NamePython, NameGo, NameJavaScript:
		return name, "", nil
	case NameTypeScript:
		return NameJavaScript, NameTypeScript, nil
	case "":
		return "", "", fmt.Errorf("profile name is required")
	default:
		return "", "", fmt.Errorf("unknown profile %q", name)
	}
}

func aliasOK(name, alias string) error {
	switch alias {
	case "":
		return nil
	case NameTypeScript:
		if name != NameJavaScript {
			return fmt.Errorf("typescript is an alias of javascript")
		}
		return nil
	default:
		return fmt.Errorf("unknown profile alias %q", alias)
	}
}

func validateArgv(name string, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("test argv is required")
	}
	if len(argv) > 16 {
		return fmt.Errorf("test argv is limited to 16 arguments")
	}
	for _, arg := range argv {
		if arg == "" || len(arg) > 200 || strings.Contains(arg, "\x00") {
			return fmt.Errorf("each test argument must be 1..200 characters and must not contain NUL")
		}
	}
	bin := argv[0]
	if bin != filepath.Base(bin) || strings.ContainsAny(bin, `/\`) {
		return fmt.Errorf("argv must name a profile runtime, not a path")
	}
	switch name {
	case NamePython:
		if bin != "python" && bin != "python3" {
			return fmt.Errorf("python profile argv must start with python or python3")
		}
	case NameGo:
		if bin != "go" {
			return fmt.Errorf("go profile argv must start with go")
		}
	case NameJavaScript:
		if bin != "node" {
			return fmt.Errorf("javascript profile argv must start with node")
		}
	default:
		return fmt.Errorf("unknown profile %q", name)
	}
	return nil
}

func cleanWorkdir(workdir string) (string, error) {
	if workdir == "" || workdir == "." {
		return ".", nil
	}
	rel, err := tools.SafeRel(workdir)
	if err != nil {
		return "", fmt.Errorf("workdir: %w", err)
	}
	return filepath.ToSlash(rel), nil
}

func cleanImage(image string) error {
	if image == "" || len(image) > 200 || strings.ContainsAny(image, " \t\r\n\x00,") {
		return fmt.Errorf("profile image is invalid")
	}
	return nil
}

func linuxArch(goarch string) (string, error) {
	if goarch == "" || strings.ContainsAny(goarch, " \t\r\n\x00/") {
		return "", fmt.Errorf("profile arch is invalid")
	}
	for _, r := range goarch {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return "", fmt.Errorf("profile arch is invalid")
		}
	}
	return "linux/" + goarch, nil
}

func allowedMemory(memory string) bool {
	switch memory {
	case "64m", "128m", "256m", "512m":
		return true
	default:
		return false
	}
}

// Arch returns the architecture recorded for this process.
func Arch() string {
	return "linux/" + runtime.GOARCH
}
