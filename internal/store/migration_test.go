package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
	"github.com/jackc/pgx/v5"
)

func TestMigration005FreshAndUpgrade(t *testing.T) {
	admin := adminConn(t)
	freshName := "agt_test_m005_fresh"
	upgradeName := "agt_test_m005_upgrade"
	recreateDB(t, admin, freshName)
	recreateDB(t, admin, upgradeName)

	t.Run("fresh", func(t *testing.T) {
		st := openStore(t, freshName)
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		conn := connectDB(t, freshName)
		defer conn.Close(context.Background())
		var column string
		err := conn.QueryRow(context.Background(),
			`SELECT data_type FROM information_schema.columns WHERE table_name='runs' AND column_name='interaction'`).Scan(&column)
		if err != nil || column != "text" {
			t.Fatalf("column %q err %v", column, err)
		}
	})

	t.Run("upgrade", func(t *testing.T) {
		conn := connectDB(t, upgradeName)
		root := testutil.RepoRoot(t)
		for _, version := range []int{1, 2, 3, 4} {
			name := fmt.Sprintf("migrations/00%d_%s.sql", version, migrationName(version))
			body, err := os.ReadFile(filepath.Join(root, "internal", "store", name))
			if err != nil {
				t.Fatal(err)
			}
			results, err := conn.PgConn().Exec(context.Background(), string(body)).ReadAll()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, result := range results {
				if result.Err != nil {
					t.Fatalf("%s: %v", name, result.Err)
				}
			}
		}
		if _, err := conn.Exec(context.Background(), `INSERT INTO schema_migrations (version) VALUES (1),(2),(3),(4)`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(context.Background(), `
			INSERT INTO runs (
				id, request_hash, goal, repo_path, state, state_version,
				budget_cents, trace_id, provider, max_steps, deadline_at
			) VALUES (
				'run_old', 'hash', 'old goal', '/tmp/repo', 'CREATED', 1,
				100, 'trace', 'fake', 4, now()
			)`); err != nil {
			t.Fatal(err)
		}
		conn.Close(context.Background())

		st := openStore(t, upgradeName)
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetRun(context.Background(), "run_old")
		if err != nil {
			t.Fatal(err)
		}
		if got.Interaction != "agent" {
			t.Fatalf("upgraded interaction %q", got.Interaction)
		}
		created, _, err := st.CreateRun(context.Background(), store.CreateInput{
			ID: "run_ask", RequestHash: "hash-ask", Goal: "ask", RepoPath: "/tmp/repo",
			BudgetCents: 100, MaxSteps: 4, Provider: "fake", Interaction: "ask",
		})
		if err != nil || created.Interaction != "ask" {
			t.Fatalf("ask create %+v err %v", created.Interaction, err)
		}
		conn = connectDB(t, upgradeName)
		defer conn.Close(context.Background())
		if _, err := conn.Exec(context.Background(), `UPDATE runs SET interaction='nope' WHERE id='run_old'`); err == nil {
			t.Fatal("check constraint accepted nope")
		}
	})
}

func migrationName(version int) string {
	switch version {
	case 1:
		return "init"
	case 2:
		return "profile"
	case 3:
		return "transcript"
	case 4:
		return "lease"
	case 5:
		return "interaction"
	case 6:
		return "sessions"
	case 7:
		return "models"
	case 8:
		return "verify"
	default:
		return ""
	}
}

func TestMigration006FreshAndUpgrade(t *testing.T) {
	admin := adminConn(t)
	freshName := "agt_test_m006_fresh"
	upgradeName := "agt_test_m006_upgrade"
	recreateDB(t, admin, freshName)
	recreateDB(t, admin, upgradeName)

	t.Run("fresh", func(t *testing.T) {
		st := openStore(t, freshName)
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		conn := connectDB(t, freshName)
		defer conn.Close(context.Background())
		var nullable string
		err := conn.QueryRow(context.Background(), `
			SELECT is_nullable FROM information_schema.columns
			WHERE table_name='runs' AND column_name='session_id'`).Scan(&nullable)
		if err != nil || nullable != "YES" {
			t.Fatalf("session_id nullable %q err %v", nullable, err)
		}
		var sessions, messages int
		if err := conn.QueryRow(context.Background(), `SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(context.Background(), `SELECT COUNT(*) FROM session_messages`).Scan(&messages); err != nil {
			t.Fatal(err)
		}
		if sessions != 0 || messages != 0 {
			t.Fatalf("fresh rows sessions %d messages %d", sessions, messages)
		}
	})

	t.Run("upgrade", func(t *testing.T) {
		conn := connectDB(t, upgradeName)
		root := testutil.RepoRoot(t)
		for _, version := range []int{1, 2, 3, 4, 5} {
			name := fmt.Sprintf("migrations/00%d_%s.sql", version, migrationName(version))
			body, err := os.ReadFile(filepath.Join(root, "internal", "store", name))
			if err != nil {
				t.Fatal(err)
			}
			results, err := conn.PgConn().Exec(context.Background(), string(body)).ReadAll()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, result := range results {
				if result.Err != nil {
					t.Fatalf("%s: %v", name, result.Err)
				}
			}
		}
		if _, err := conn.Exec(context.Background(), `INSERT INTO schema_migrations (version) VALUES (1),(2),(3),(4),(5)`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(context.Background(), `
			INSERT INTO runs (
				id, request_hash, goal, repo_path, state, state_version,
				budget_cents, trace_id, provider, max_steps, deadline_at
			) VALUES (
				'run_legacy', 'hash', 'legacy goal', '/tmp/repo', 'CREATED', 1,
				100, 'trace', 'fake', 4, now()
			)`); err != nil {
			t.Fatal(err)
		}
		conn.Close(context.Background())

		st := openStore(t, upgradeName)
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetRun(context.Background(), "run_legacy")
		if err != nil {
			t.Fatal(err)
		}
		if got.SessionID != "" || got.Goal != "legacy goal" || got.Interaction != "agent" {
			t.Fatalf("legacy run %+v", got)
		}
		sess, err := st.CreateSession(context.Background(), "/tmp/repo", "")
		if err != nil || sess.RepoPath != "/tmp/repo" {
			t.Fatalf("session %+v err %v", sess, err)
		}
		if err := st.RecordSessionResult(context.Background(), got); err != nil {
			t.Fatal(err)
		}
		msgs, err := st.ListSessionMessages(context.Background(), sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 0 {
			t.Fatalf("legacy run wrote a session message: %+v", msgs)
		}
	})
}

func TestMigration007FreshAndUpgrade(t *testing.T) {
	admin := adminConn(t)
	freshName := "agt_test_m007_fresh"
	upgradeName := "agt_test_m007_upgrade"
	recreateDB(t, admin, freshName)
	recreateDB(t, admin, upgradeName)

	t.Run("fresh", func(t *testing.T) {
		st := openStore(t, freshName)
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		conn := connectDB(t, freshName)
		defer conn.Close(context.Background())
		for _, column := range []string{"model_config", "model_name", "model_endpoint", "input_per_million", "output_per_million", "pricing_recorded", "credential_env"} {
			var count int
			err := conn.QueryRow(context.Background(), `
				SELECT COUNT(*) FROM information_schema.columns
				WHERE table_name='runs' AND column_name=$1`, column).Scan(&count)
			if err != nil || count != 1 {
				t.Fatalf("runs.%s count %d err %v", column, count, err)
			}
		}
		var sessionModel int
		err := conn.QueryRow(context.Background(), `
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name='sessions' AND column_name='model_config'`).Scan(&sessionModel)
		if err != nil || sessionModel != 1 {
			t.Fatalf("sessions.model_config %d err %v", sessionModel, err)
		}
	})

	t.Run("upgrade", func(t *testing.T) {
		conn := connectDB(t, upgradeName)
		root := testutil.RepoRoot(t)
		for _, version := range []int{1, 2, 3, 4, 5, 6} {
			name := fmt.Sprintf("migrations/00%d_%s.sql", version, migrationName(version))
			body, err := os.ReadFile(filepath.Join(root, "internal", "store", name))
			if err != nil {
				t.Fatal(err)
			}
			results, err := conn.PgConn().Exec(context.Background(), string(body)).ReadAll()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, result := range results {
				if result.Err != nil {
					t.Fatalf("%s: %v", name, result.Err)
				}
			}
		}
		if _, err := conn.Exec(context.Background(), `INSERT INTO schema_migrations (version) VALUES (1),(2),(3),(4),(5),(6)`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(context.Background(), `
			INSERT INTO runs (
				id, request_hash, goal, repo_path, state, state_version,
				budget_cents, trace_id, provider, max_steps, deadline_at
			) VALUES (
				'run_pre_model', 'hash', 'legacy goal', '/tmp/repo', 'CREATED', 1,
				100, 'trace', 'fake', 4, now()
			)`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(context.Background(), `
			INSERT INTO sessions (id, repo_path, title) VALUES ('ses_pre_model', '/tmp/repo', 'old')`); err != nil {
			t.Fatal(err)
		}
		conn.Close(context.Background())

		st := openStore(t, upgradeName)
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetRun(context.Background(), "run_pre_model")
		if err != nil {
			t.Fatal(err)
		}
		if got.ModelConfig != "" || got.ModelName != "" || got.ModelEndpoint != "" || got.PricingRecorded || got.CredentialEnv != "" || got.Goal != "legacy goal" {
			t.Fatalf("legacy run %+v", got)
		}
		sess, err := st.GetSession(context.Background(), "ses_pre_model")
		if err != nil || sess.ModelConfig != "" {
			t.Fatalf("legacy session %+v err %v", sess, err)
		}
		if err := st.SetSessionModel(context.Background(), sess.ID, "local"); err != nil {
			t.Fatal(err)
		}
		sess, err = st.GetSession(context.Background(), sess.ID)
		if err != nil || sess.ModelConfig != "local" {
			t.Fatalf("session preference %+v err %v", sess, err)
		}
		created, _, err := st.CreateRun(context.Background(), store.CreateInput{
			Goal: "new", RepoPath: "/tmp/repo", BudgetCents: 10, MaxSteps: 1, WallSeconds: 30,
			Provider: "openai", RequestHash: "hash-new", ProfileJSON: []byte(`{}`),
			ModelConfig: "local", ModelName: "local-repair", ModelEndpoint: "http://127.0.0.1:9/v1",
			InputPerMillion: 3_000_000, OutputPerMillion: 1, PricingRecorded: true, CredentialEnv: "AGT_CLOUD_KEY",
		})
		if err != nil {
			t.Fatal(err)
		}
		if created.ModelConfig != "local" || created.ModelName != "local-repair" || created.ModelEndpoint != "http://127.0.0.1:9/v1" || !created.PricingRecorded || created.InputPerMillion != 3_000_000 {
			t.Fatalf("snapshot %+v", created)
		}
		legacy, err := st.GetRun(context.Background(), "run_pre_model")
		if err != nil || legacy.ModelConfig != "" || legacy.PricingRecorded {
			t.Fatalf("legacy changed %+v err %v", legacy, err)
		}
	})
}

func TestMigration008FreshAndUpgrade(t *testing.T) {
	admin := adminConn(t)
	freshName := "agt_test_m008_fresh"
	upgradeName := "agt_test_m008_upgrade"
	recreateDB(t, admin, freshName)
	recreateDB(t, admin, upgradeName)

	t.Run("fresh", func(t *testing.T) {
		st := openStore(t, freshName)
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		conn := connectDB(t, freshName)
		defer conn.Close(context.Background())
		for _, column := range []string{"task_type", "contract_json", "contract_digest", "baseline_json", "final_json", "verification_outcome", "verification_digest"} {
			var count int
			err := conn.QueryRow(context.Background(), `
				SELECT COUNT(*) FROM information_schema.columns
				WHERE table_name='runs' AND column_name=$1`, column).Scan(&count)
			if err != nil || count != 1 {
				t.Fatalf("runs.%s count %d err %v", column, count, err)
			}
		}
		created, _, err := st.CreateRun(context.Background(), store.CreateInput{
			Goal: "repair", RepoPath: "/tmp/repo", BudgetCents: 10, MaxSteps: 1, WallSeconds: 30,
			Provider: "fake", RequestHash: "hash-repair", ProfileJSON: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if created.TaskType != "repair" || created.ContractDigest != "" || string(created.ContractJSON) != "null" {
			t.Fatalf("fresh repair %+v contract %s", created.TaskType, created.ContractJSON)
		}
	})

	t.Run("upgrade", func(t *testing.T) {
		conn := connectDB(t, upgradeName)
		root := testutil.RepoRoot(t)
		for _, version := range []int{1, 2, 3, 4, 5, 6, 7} {
			name := fmt.Sprintf("migrations/00%d_%s.sql", version, migrationName(version))
			body, err := os.ReadFile(filepath.Join(root, "internal", "store", name))
			if err != nil {
				t.Fatal(err)
			}
			results, err := conn.PgConn().Exec(context.Background(), string(body)).ReadAll()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, result := range results {
				if result.Err != nil {
					t.Fatalf("%s: %v", name, result.Err)
				}
			}
		}
		if _, err := conn.Exec(context.Background(), `INSERT INTO schema_migrations (version) VALUES (1),(2),(3),(4),(5),(6),(7)`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(context.Background(), `
			INSERT INTO runs (
				id, request_hash, goal, repo_path, state, state_version,
				budget_cents, trace_id, provider, max_steps, deadline_at
			) VALUES (
				'run_pre_verify', 'hash', 'legacy goal', '/tmp/repo', 'CREATED', 1,
				100, 'trace', 'fake', 4, now()
			)`); err != nil {
			t.Fatal(err)
		}
		conn.Close(context.Background())

		st := openStore(t, upgradeName)
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetRun(context.Background(), "run_pre_verify")
		if err != nil {
			t.Fatal(err)
		}
		if got.TaskType != "repair" || got.ContractDigest != "" || got.VerificationOutcome != "" || got.VerificationDigest != "" || got.Goal != "legacy goal" || string(got.ContractJSON) != "null" {
			t.Fatalf("legacy run task %s digest %q outcome %q goal %s contract %s", got.TaskType, got.ContractDigest, got.VerificationOutcome, got.Goal, got.ContractJSON)
		}
		if err := st.SetResult(context.Background(), got.ID, map[string]any{"outcome": "repaired"}, ""); err != nil {
			t.Fatal(err)
		}
		if err := st.SetVerification(context.Background(), got.ID, []byte(`{"phase":"final"}`), "checks_passed", "abc"); err != nil {
			t.Fatal(err)
		}
		got, err = st.GetRun(context.Background(), got.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ContractDigest != "" || got.VerificationOutcome != "checks_passed" || got.VerificationDigest != "abc" || string(got.ContractJSON) != "null" {
			t.Fatalf("result write revised the contract: %+v", got)
		}
		if err := st.SetBaseline(context.Background(), got.ID, []byte(`{"phase":"baseline"}`)); err != nil {
			t.Fatal(err)
		}
		if err := st.SetBaseline(context.Background(), got.ID, []byte(`{"phase":"other"}`)); err == nil {
			t.Fatal("baseline was replaced")
		}
		got, err = st.GetRun(context.Background(), got.ID)
		if err != nil || !strings.Contains(string(got.BaselineJSON), "baseline") || got.ContractDigest != "" {
			t.Fatalf("baseline %+v err %v", string(got.BaselineJSON), err)
		}
	})
}

func adminConn(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, testutil.ServerURL())
	if err != nil {
		if testutil.MissingPrereq(os.Getenv("AGT_DATABASE_URL") != "") {
			t.Fatalf("postgres: %v", err)
		}
		t.Skipf("postgres unreachable: %v", err)
	}
	var current string
	if err := conn.QueryRow(context.Background(), `SELECT current_database()`).Scan(&current); err != nil {
		conn.Close(context.Background())
		t.Fatal(err)
	}
	if current != "postgres" {
		conn.Close(context.Background())
		t.Fatalf("admin connection is database %q", current)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func recreateDB(t *testing.T, admin *pgx.Conn, name string) {
	t.Helper()
	quoted, err := store.QuoteDisposable(name)
	if err != nil {
		t.Fatalf("refusing to recreate %s: %v", name, err)
	}
	var current string
	if err := admin.QueryRow(context.Background(), `SELECT current_database()`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != "postgres" {
		t.Fatalf("refusing to drop from database %s", current)
	}
	if _, err := admin.Exec(context.Background(), `DROP DATABASE IF EXISTS `+quoted+` WITH (FORCE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `CREATE DATABASE `+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		quoted, err := store.QuoteDisposable(name)
		if err != nil {
			return
		}
		_, _ = admin.Exec(context.Background(), `DROP DATABASE IF EXISTS `+quoted+` WITH (FORCE)`)
	})
}

func connectDB(t *testing.T, name string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dbURL(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func openStore(t *testing.T, name string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), dbURL(t, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func dbURL(t *testing.T, name string) string {
	t.Helper()
	raw, err := testutil.WithDatabase(testutil.ServerURL(), name)
	if err != nil {
		t.Fatal(err)
	}
	if name == "agt" || strings.HasSuffix(strings.Split(raw, "?")[0], "/agt") {
		t.Fatalf("refusing database URL %s", raw)
	}
	return raw
}
