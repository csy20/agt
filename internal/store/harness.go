package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// HarnessPurpose marks a database created for one integration test.
// Production migrations do not create this table.
const HarnessPurpose = "agt-integration-test"

var disposableName = regexp.MustCompile(`^agt_test_[a-z0-9_]{6,40}$`)

// DisposableName reports whether name is a database this process may reset or drop.
// The shared development database "agt" is never disposable.
func DisposableName(name string) bool {
	switch strings.TrimSpace(name) {
	case "", "agt", "postgres", "template0", "template1", "AGT", "agt_test", "agt_m007":
		return false
	}
	if strings.HasPrefix(name, "agt_m00") {
		return false
	}
	return disposableName.MatchString(name)
}

// AuthorizeReset is the pure ownership check for Reset and DROP DATABASE.
// An empty token fails before any caller needs to query a database.
func AuthorizeReset(token, dbName, markerToken, purpose string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("reset is not armed")
	}
	if !DisposableName(dbName) {
		return fmt.Errorf("refusing to reset database %q", dbName)
	}
	if markerToken != token || purpose != HarnessPurpose {
		return fmt.Errorf("database %q is not an armed integration database", dbName)
	}
	return nil
}

// QuoteDisposable quotes a disposable database name for SQL.
// It returns an error for every other name, including "agt".
func QuoteDisposable(name string) (string, error) {
	if !DisposableName(name) || strings.ContainsAny(name, `"\`) {
		return "", fmt.Errorf("refusing identifier %q", name)
	}
	return `"` + name + `"`, nil
}

// MarkTestDatabase writes the harness marker and arms Reset.
// A database whose name is not disposable is left unchanged.
func (s *Store) MarkTestDatabase(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("test token is empty")
	}
	var name string
	if err := s.pool.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		return err
	}
	if !DisposableName(name) {
		return fmt.Errorf("refusing to mark database %q", name)
	}
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS agt_harness (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO agt_harness (key, value) VALUES ('token', $1), ('purpose', $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, token, HarnessPurpose); err != nil {
		return err
	}
	s.resetToken = token
	return nil
}
