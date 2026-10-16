package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/csy20/agt/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const defaultURL = "postgres://agt:agt@127.0.0.1:54329/agt?sslmode=disable"

type ownedDB struct {
	URL   string
	Name  string
	Token string
}

var owned sync.Map

// ServerURL is the Postgres maintenance database on the configured server.
// The path of AGT_DATABASE_URL is discarded. This function never returns
// database "agt".
func ServerURL() string {
	raw := strings.TrimSpace(os.Getenv("AGT_DATABASE_URL"))
	if raw == "" {
		raw = defaultURL
	}
	rewritten, err := WithDatabase(raw, "postgres")
	if err != nil {
		return "postgres://127.0.0.1:1/postgres?sslmode=disable"
	}
	return rewritten
}

// DatabaseURL is the maintenance connection used to create disposable
// databases. Callers must not migrate or reset it. Open does not use it
// as a store URL.
func DatabaseURL() string { return ServerURL() }

// URL is the disposable database owned by st. It is empty for a store this
// process did not open.
func URL(st *store.Store) string {
	if st == nil {
		return ""
	}
	value, ok := owned.Load(st)
	if !ok {
		return ""
	}
	return value.(ownedDB).URL
}

// WithDatabase replaces the database path. The only allowed names are
// "postgres" and a disposable test name. Database "agt" is refused.
func WithDatabase(raw, name string) (string, error) {
	if name != "postgres" && !store.DisposableName(name) {
		return "", fmt.Errorf("refusing database %q", name)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", fmt.Errorf("database URL scheme %q is not postgres", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("database URL has no host")
	}
	parsed.Path = "/" + name
	parsed.RawPath = ""
	return parsed.String(), nil
}

// Open migrates a new disposable database and arms Reset with a harness token.
// It does not take the shared advisory lock and it does not open database "agt".
// Cleanup drops only that database, and only after the harness token matches.
func Open(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("AGT_DATA_DIR", t.TempDir())
	explicit := os.Getenv("AGT_DATABASE_URL") != ""
	adminURL := ServerURL()
	dial, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	admin, err := pgx.Connect(dial, adminURL)
	cancel()
	if err != nil {
		if MissingPrereq(explicit) {
			t.Fatalf("postgres: %v", err)
		}
		t.Skipf("postgres unreachable: %v", err)
	}
	var current string
	if err := admin.QueryRow(context.Background(), `SELECT current_database()`).Scan(&current); err != nil {
		admin.Close(context.Background())
		t.Fatalf("current database: %v", err)
	}
	if current != "postgres" {
		admin.Close(context.Background())
		t.Fatalf("admin connection is database %q", current)
	}
	name, err := newDisposableName()
	if err != nil {
		admin.Close(context.Background())
		t.Fatal(err)
	}
	if err := createDisposable(admin, name); err != nil {
		admin.Close(context.Background())
		if MissingPrereq(explicit) {
			t.Fatalf("create database %s: %v", name, err)
		}
		t.Skipf("create database %s: %v", name, err)
	}
	admin.Close(context.Background())

	token, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	dbURL, err := WithDatabase(adminURL, name)
	if err != nil {
		t.Fatal(err)
	}
	if databaseName(dbURL) == "agt" || databaseName(dbURL) == "postgres" {
		t.Fatalf("refusing store URL %s", dbURL)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := dropDisposable(ctx, adminURL, name, token); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})
	if err := markHarness(dbURL, token); err != nil {
		t.Fatalf("mark %s: %v", name, err)
	}

	st, err := store.Open(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.MarkTestDatabase(context.Background(), token); err != nil {
		t.Fatalf("arm: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.Reset(context.Background()); err != nil {
		t.Fatalf("reset: %v", err)
	}
	owned.Store(st, ownedDB{URL: dbURL, Name: name, Token: token})
	return st
}

func newDisposableName() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	name := "agt_test_" + hex.EncodeToString(buf)
	if !store.DisposableName(name) {
		return "", fmt.Errorf("generated name %q is not disposable", name)
	}
	return name, nil
}

func newToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func createDisposable(admin *pgx.Conn, name string) error {
	quoted, err := store.QuoteDisposable(name)
	if err != nil {
		return err
	}
	_, err = admin.Exec(context.Background(), `CREATE DATABASE `+quoted)
	return err
}

func markHarness(dbURL, token string) error {
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			cancel()
			last = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var name string
		err = conn.QueryRow(ctx, `SELECT current_database()`).Scan(&name)
		if err == nil && !store.DisposableName(name) {
			err = fmt.Errorf("refusing to mark database %q", name)
		}
		if err == nil {
			_, err = conn.Exec(ctx, `
				CREATE TABLE IF NOT EXISTS agt_harness (
					key TEXT PRIMARY KEY,
					value TEXT NOT NULL
				)`)
		}
		if err == nil {
			_, err = conn.Exec(ctx, `
				INSERT INTO agt_harness (key, value) VALUES ('token', $1), ('purpose', $2)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, token, store.HarnessPurpose)
		}
		conn.Close(context.Background())
		cancel()
		return err
	}
	return last
}

func dropDisposable(ctx context.Context, adminURL, name, token string) error {
	quoted, err := store.QuoteDisposable(name)
	if err != nil {
		return err
	}
	dbURL, err := WithDatabase(adminURL, name)
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		if missingDatabase(err) {
			return nil
		}
		return fmt.Errorf("refusing to drop %s: %w", name, err)
	}
	var harness, migrations *string
	qerr := conn.QueryRow(ctx, `SELECT to_regclass('public.agt_harness'), to_regclass('public.schema_migrations')`).Scan(&harness, &migrations)
	var marker, purpose string
	if qerr == nil && harness != nil {
		qerr = conn.QueryRow(ctx, `
			SELECT
				COALESCE((SELECT value FROM agt_harness WHERE key = 'token'), ''),
				COALESCE((SELECT value FROM agt_harness WHERE key = 'purpose'), '')`).Scan(&marker, &purpose)
	}
	conn.Close(context.Background())
	if qerr != nil {
		return fmt.Errorf("refusing to drop %s: %w", name, qerr)
	}
	switch {
	case harness == nil && migrations == nil:
		// This test created the database and nothing has migrated it.
	case harness != nil:
		if err := store.AuthorizeReset(token, name, marker, purpose); err != nil {
			return err
		}
	default:
		return fmt.Errorf("refusing to drop %s: it has a schema and no harness marker", name)
	}
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer admin.Close(context.Background())
	var current string
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&current); err != nil {
		return err
	}
	if current != "postgres" {
		return fmt.Errorf("refusing to drop %s from database %s", name, current)
	}
	_, err = admin.Exec(ctx, `DROP DATABASE `+quoted+` WITH (FORCE)`)
	if err != nil && missingDatabase(err) {
		return nil
	}
	return err
}

func missingDatabase(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "3D000" {
		return true
	}
	return strings.Contains(err.Error(), "does not exist")
}

func databaseName(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.Trim(parsed.Path, "/")
}
