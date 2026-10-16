package store_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
	"github.com/jackc/pgx/v5"
)

func TestResetRefusesAnUnarmedStore(t *testing.T) {
	armed := testutil.Open(t)
	ctx := context.Background()
	sess, err := armed.CreateSession(ctx, "/tmp/keep", "keep-me")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Open(ctx, testutil.URL(armed))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	if err := other.Reset(ctx); err == nil {
		t.Fatal("unarmed store reset a database")
	}
	got, err := armed.GetSession(ctx, sess.ID)
	if err != nil || got.Title != "keep-me" {
		t.Fatalf("session after refused reset %+v %v", got, err)
	}
	if err := armed.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := armed.GetSession(ctx, sess.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("armed reset err %v", err)
	}
}

func TestMarkRefusesTheMaintenanceDatabase(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testutil.ServerURL())
	if err != nil {
		if testutil.MissingPrereq(os.Getenv("AGT_DATABASE_URL") != "") {
			t.Fatal(err)
		}
		t.Skipf("postgres unreachable: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.MarkTestDatabase(ctx, "token-value"); err == nil {
		t.Fatal("marked the maintenance database")
	}
	if err := st.Reset(ctx); err == nil {
		t.Fatal("reset the maintenance database")
	}
	conn, err := pgx.Connect(ctx, testutil.ServerURL())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var current string
	var rel *string
	if err := conn.QueryRow(ctx, `SELECT current_database(), to_regclass('public.agt_harness')`).Scan(&current, &rel); err != nil {
		t.Fatal(err)
	}
	if current != "postgres" || rel != nil {
		t.Fatalf("maintenance database %s harness %v", current, rel)
	}
}
