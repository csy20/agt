package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/csy20/agt/internal/auth"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/diag"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/version"
)

type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Next   string `json:"next,omitempty"`
}

type doctorReport struct {
	OK     bool          `json:"ok"`
	Checks []doctorCheck `json:"checks"`
	Next   string        `json:"next,omitempty"`
	Paths  doctorPaths   `json:"paths"`
}

type doctorPaths struct {
	Data   string `json:"data"`
	Config string `json:"config"`
	Cache  string `json:"cache"`
	Log    string `json:"log"`
	Listen string `json:"listen"`
}

func doctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	server := fs.String("server", "", "agtd base URL")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true})); err != nil {
		return 2
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: agt doctor [--json] [--server URL]")
		return 2
	}
	cfg := config.Load()
	base, _ := clientBase(*server)
	report := makeDoctorReport(cfg, base, liveDoctor)
	if *asJSON {
		raw, err := json.Marshal(report)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(string(raw))
	} else {
		fmt.Print(formatDoctor(report))
	}
	if report.OK {
		return 0
	}
	return 1
}

type doctorFns struct {
	listen   func(string) doctorCheck
	dataDir  func(string) doctorCheck
	token    func(string) doctorCheck
	model    func(config.Config) doctorCheck
	docker   func([]string) []doctorCheck
	postgres func(string) doctorCheck
	daemon   func(string) doctorCheck
}

func liveDoctor() doctorFns {
	return doctorFns{
		listen:   probeListen,
		dataDir:  probeDataDir,
		token:    probeToken,
		model:    probeModel,
		docker:   probeDocker,
		postgres: probePostgres,
		daemon:   probeDaemon,
	}
}

func makeDoctorReport(cfg config.Config, base string, fns func() doctorFns) doctorReport {
	places := config.CurrentPlaces()
	probe := fns()
	images := []string{cfg.ImagePython, cfg.ImageGo, cfg.ImageNode}
	checks := []doctorCheck{
		probe.listen(cfg.ListenAddr),
		probe.model(cfg),
	}
	checks = append(checks, probe.docker(images)...)
	checks = append(checks,
		probe.postgres(cfg.DatabaseURL),
		probe.dataDir(places.Data),
		probe.token(places.Data),
		probe.daemon(base),
	)
	report := assembleDoctor(checks)
	report.Paths = doctorPaths{
		Data: places.Data, Config: places.Config, Cache: places.Cache,
		Log: places.Log, Listen: cfg.ListenAddr,
	}
	return report
}

func assembleDoctor(checks []doctorCheck) doctorReport {
	report := doctorReport{OK: true, Checks: checks}
	for _, check := range checks {
		if check.OK {
			continue
		}
		report.OK = false
		if report.Next == "" {
			report.Next = check.Next
		}
	}
	return report
}

func formatDoctor(report doctorReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "data    %s\n", report.Paths.Data)
	fmt.Fprintf(&b, "config  %s\n", report.Paths.Config)
	fmt.Fprintf(&b, "cache   %s\n", report.Paths.Cache)
	fmt.Fprintf(&b, "log     %s\n", report.Paths.Log)
	fmt.Fprintf(&b, "listen  %s\n", report.Paths.Listen)
	for _, check := range report.Checks {
		mark := "ok"
		if !check.OK {
			mark = "fail"
		}
		fmt.Fprintf(&b, "%s  %s  %s\n", mark, check.Name, check.Detail)
	}
	if report.Next != "" {
		fmt.Fprintf(&b, "next  %s\n", report.Next)
	}
	return b.String()
}

func probeListen(addr string) doctorCheck {
	if err := config.ValidateListenAddr(addr); err != nil {
		return doctorCheck{Name: "listen", OK: false, Detail: err.Error(), Next: "Set AGT_LISTEN to 127.0.0.1:8787, ::1:8787, or localhost:8787. Then run `agt doctor`"}
	}
	return doctorCheck{Name: "listen", OK: true, Detail: addr + " is loopback"}
}

func probeDataDir(dir string) doctorCheck {
	info, err := os.Stat(dir)
	if err != nil {
		return doctorCheck{Name: "data_dir", OK: false, Detail: dir + " does not exist", Next: "agtd creates " + dir + " at mode 0700 when it starts. The client and daemon share AGT_DATA_DIR unless AGT_TOKEN is set. Then run `agt doctor`"}
	}
	if !info.IsDir() {
		return doctorCheck{Name: "data_dir", OK: false, Detail: dir + " is not a directory", Next: "Set AGT_DATA_DIR to a directory and run `agt doctor`"}
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return doctorCheck{Name: "data_dir", OK: false, Detail: fmt.Sprintf("%s mode is %04o", dir, info.Mode().Perm()), Next: "Run `chmod 700 " + dir + "`, then `agt doctor`"}
	}
	return doctorCheck{Name: "data_dir", OK: true, Detail: dir}
}

func probeToken(dir string) doctorCheck {
	path := filepath.Join(dir, "auth.token")
	if strings.TrimSpace(os.Getenv("AGT_TOKEN")) != "" {
		if _, err := auth.Read(dir); err != nil {
			return doctorCheck{Name: "token", OK: false, Detail: err.Error(), Next: "Set AGT_TOKEN to 16..256 visible ASCII characters. Then run `agt doctor`"}
		}
		return doctorCheck{Name: "token", OK: true, Detail: "AGT_TOKEN is set"}
	}
	if err := auth.FilePrivate(dir); err != nil {
		if os.IsNotExist(err) {
			return doctorCheck{Name: "token", OK: false, Detail: "no token file at " + path, Next: "Start agtd with this data directory so it can create auth.token at mode 0600. Use the same AGT_DATA_DIR for agt, or set AGT_TOKEN. Then run `agt doctor`"}
		}
		return doctorCheck{Name: "token", OK: false, Detail: err.Error(), Next: "Remove a symlinked auth.token, or run `chmod 600 " + path + "`. agtd also replaces a group- or world-readable token when it starts. Then run `agt doctor`"}
	}
	info, err := os.Stat(path)
	mode := ""
	if err == nil {
		mode = fmt.Sprintf(" mode %04o", info.Mode().Perm())
	}
	return doctorCheck{Name: "token", OK: true, Detail: path + mode}
}

func probeModel(cfg config.Config) doctorCheck {
	msg := diag.ModelConfigWarning(cfg.Provider, cfg.ModelAPIKey, cfg.ModelName, cfg.ModelBaseURL)
	detail := "provider " + cfg.Provider
	if cfg.ModelName != "" {
		detail += " model " + cfg.ModelName
	}
	if msg == "" {
		return doctorCheck{Name: "model", OK: true, Detail: detail}
	}
	return doctorCheck{Name: "model", OK: false, Detail: msg, Next: msg}
}

func probeDocker(images []string) []doctorCheck {
	if _, err := exec.LookPath("docker"); err != nil {
		return []doctorCheck{
			{Name: "docker", OK: false, Detail: "docker was not found on PATH", Next: "Install Docker and start its daemon. agt runs tests only inside a container and does not fall back to the host. Then run `agt doctor`"},
			{Name: "images", OK: false, Detail: "not checked", Next: "Install Docker, then run `make image` and `agt doctor`"},
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "info").CombinedOutput(); err != nil {
		return []doctorCheck{
			{Name: "docker", OK: false, Detail: "docker is installed but the daemon is not reachable " + clipDoctor(out), Next: "Start Docker, then run `agt doctor`"},
			{Name: "images", OK: false, Detail: "not checked", Next: "Start Docker, then run `make image` and `agt doctor`"},
		}
	}
	var missing []string
	for _, image := range images {
		if strings.TrimSpace(image) == "" {
			missing = append(missing, "(empty image name)")
			continue
		}
		if out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).CombinedOutput(); err != nil {
			missing = append(missing, image+" "+clipDoctor(out))
		}
	}
	if len(missing) > 0 {
		return []doctorCheck{
			{Name: "docker", OK: true, Detail: "docker daemon is reachable"},
			{Name: "images", OK: false, Detail: "missing sandbox images: " + strings.Join(missing, "; "), Next: "Build the sandbox images with `make image`. Then run `agt doctor`"},
		}
	}
	return []doctorCheck{
		{Name: "docker", OK: true, Detail: "docker daemon is reachable"},
		{Name: "images", OK: true, Detail: strings.Join(images, ", ")},
	}
}

func probePostgres(databaseURL string) doctorCheck {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := store.Open(ctx, databaseURL)
	if err != nil {
		return doctorCheck{Name: "postgres", OK: false, Detail: err.Error(), Next: "Start Postgres with `docker compose -f deploy/compose.yaml up -d --wait` or set AGT_DATABASE_URL. Then run `agt doctor`"}
	}
	st.Close()
	return doctorCheck{Name: "postgres", OK: true, Detail: "reachable at " + diag.RedactDatabaseURL(databaseURL)}
}

func probeDaemon(base string) doctorCheck {
	err := discover(base)
	note := daemonProcessNote()
	if err == nil {
		detail := "agtd " + version.Product + " at " + base
		if note != "" {
			detail += ". " + note
		}
		return doctorCheck{Name: "daemon", OK: true, Detail: detail}
	}
	detail := err.Error()
	if note != "" {
		detail += " " + note
	}
	next := "Start agtd, or place agtd beside agt and run a command. The daemon keeps running after agt exits. Ctrl+C during `agt run attach` only detaches. `agt run cancel RUN` cancels a run."
	if !errors.Is(err, errDaemonDown) {
		next = err.Error()
	}
	return doctorCheck{Name: "daemon", OK: false, Detail: detail, Next: next}
}

func daemonProcessNote() string {
	path := filepath.Join(config.Load().DataDir, "agtd.pid")
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		return "The pid file at " + path + " is not a number."
	}
	if processAlive(pid) {
		return fmt.Sprintf("pid %d is still running, so leaving agt does not stop agtd.", pid)
	}
	return fmt.Sprintf("pid file %d is stale.", pid)
}

func clipDoctor(out []byte) string {
	s := strings.TrimSpace(string(out))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 180 {
		s = s[:180] + "..."
	}
	if s == "" {
		return ""
	}
	return "(" + s + ")"
}
