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
	freshName := "agt_m005_fresh"
	upgradeName := "agt_m005_upgrade"
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
	default:
		return ""
	}
}

func TestMigration006FreshAndUpgrade(t *testing.T) {
	admin := adminConn(t)
	freshName := "agt_m006_fresh"
	upgradeName := "agt_m006_upgrade"
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

func adminConn(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, testutil.DatabaseURL())
	if err != nil {
		if testutil.MissingPrereq(os.Getenv("AGT_DATABASE_URL") != "") {
			t.Fatalf("postgres: %v", err)
		}
		t.Skipf("postgres unreachable: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func recreateDB(t *testing.T, admin *pgx.Conn, name string) {
	t.Helper()
	if _, err := admin.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
}

func connectDB(t *testing.T, name string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), swapDB(testutil.DatabaseURL(), name))
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func openStore(t *testing.T, name string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), swapDB(testutil.DatabaseURL(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func swapDB(raw, name string) string {
	return strings.Replace(raw, "/agt?", "/"+name+"?", 1)
}
