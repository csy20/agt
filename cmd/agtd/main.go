package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/csy20/agt/internal/api"
	"github.com/csy20/agt/internal/auth"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/diag"
	"github.com/csy20/agt/internal/model"
	"github.com/csy20/agt/internal/orch"
	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/sandbox"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/telemetry"
	"github.com/csy20/agt/internal/usage"
	"github.com/csy20/agt/internal/workspace"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg := config.Load()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := config.ValidateListenAddr(cfg.ListenAddr); err != nil {
		slog.Error(err.Error())
		return 1
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		slog.Error("data dir", "err", err)
		return 1
	}
	token, err := auth.LoadOrCreate(cfg.DataDir)
	if err != nil {
		slog.Error("local token", "err", err)
		return 1
	}
	if token == "" {
		slog.Error("local token is empty")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("postgres", "err", err)
		return 1
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		slog.Error("migrate", "err", err)
		return 1
	}
	if msg := diag.ModelConfigWarning(cfg.Provider, cfg.ModelAPIKey, cfg.ModelName, cfg.ModelBaseURL); msg != "" {
		slog.Warn(msg)
	}
	// Tests run only in this Docker runner. There is no host-process fallback.
	runner := &sandbox.Runner{Images: cfg.ProfileImages()}
	checkCtx, cancelCheck := context.WithTimeout(ctx, 20*time.Second)
	err = runner.Preflight(checkCtx)
	cancelCheck()
	if err != nil {
		slog.Error(err.Error())
		return 1
	}
	engine := orch.New(orch.Options{
		Store:   st,
		DataDir: cfg.DataDir,
		Prices: usage.Prices{
			InputPerMillion:  cfg.InputPerMillion,
			OutputPerMillion: cfg.OutputPerMillion,
		},
		Tel: telemetry.New(cfg.OTLPEndpoint),
		ModelFor: func(current store.Run) (model.Provider, error) {
			switch current.Provider {
			case "fake", "":
				script, err := model.ParseScript(current.ScriptJSON)
				if err != nil {
					return nil, err
				}
				return model.NewFake(script), nil
			case "openai":
				return model.NewOpenAI(cfg.ModelBaseURL, cfg.ModelAPIKey, cfg.ModelName), nil
			default:
				return nil, fmt.Errorf("unknown provider %s", current.Provider)
			}
		},
		NewWorkspace: func(current store.Run) (workspace.Handle, error) {
			prof, err := profile.Parse(current.ProfileJSON)
			if err != nil {
				return nil, err
			}
			dir := workspace.NewDir(current.Worktree, orch.BasePath(current.Worktree), func(ctx context.Context, root string) (string, string, int, error) {
				return runner.RunProfile(ctx, root, current.ID, prof)
			})
			dir.CloseFn = func(ctx context.Context) error {
				return runner.Destroy(ctx, current.ID)
			}
			return dir, nil
		},
	})
	worker := &orch.Worker{Store: st, Run: engine.Run}
	go worker.Loop(ctx)
	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.New(st, cfg, token).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shut)
	}()
	slog.Info("listening", "addr", cfg.ListenAddr, "data_dir", cfg.DataDir)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("http", "err", err)
		return 1
	}
	return 0
}
