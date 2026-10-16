package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
	"github.com/jackc/pgx/v5"
)

func TestControlDaemonSurvivesAndCannotClaimIntegrationJobs(t *testing.T) {
	if store.DisposableName("agt") {
		t.Fatal("database agt was treated as disposable")
	}
	control := testutil.Open(t)
	controlURL := testutil.URL(control)
	if controlURL == "" || strings.Contains(controlURL, "/agt?") || !strings.Contains(controlURL, "/agt_test_") {
		t.Fatalf("control URL %s", controlURL)
	}
	ctx := context.Background()
	sess, err := control.CreateSession(ctx, "/tmp/control-repo", "control-title")
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := control.CreateRun(ctx, store.CreateInput{
		Goal: "control", RepoPath: "/tmp/control-repo", BudgetCents: 10, MaxSteps: 1,
		WallSeconds: 30, Provider: "fake", RequestHash: "control-hash",
		ProfileJSON: []byte(`{}`), Interaction: "agent", SessionID: sess.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteJobs(t, controlURL, created.ID); err != nil {
		t.Fatal(err)
	}

	root := testutil.RepoRoot(t)
	binDir := t.TempDir()
	agtdBin := buildBin(t, root, binDir, "agtd", "./cmd/agtd")
	addr := freeAddr(t)
	data := t.TempDir()
	cmd := startAgtd(t, agtdBin, data, addr, controlURL)
	t.Cleanup(func() { stopProcess(cmd) })

	integration := testutil.Open(t)
	integrationURL := testutil.URL(integration)
	if integrationURL == controlURL || strings.Contains(integrationURL, "/agt?") {
		t.Fatalf("integration URL %s", integrationURL)
	}
	job, _, err := integration.CreateRun(ctx, store.CreateInput{
		Goal: "integration", RepoPath: "/tmp/integration-repo", BudgetCents: 10, MaxSteps: 1,
		WallSeconds: 30, Provider: "fake", RequestHash: "integration-hash",
		ProfileJSON: []byte(`{}`), Interaction: "ask",
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)

	status, owner := jobStatus(t, integrationURL, job.ID)
	if status != "queued" || owner != "" {
		t.Fatalf("integration job status %q owner %q", status, owner)
	}
	gotSess, err := control.GetSession(ctx, sess.ID)
	if err != nil || gotSess.Title != "control-title" {
		t.Fatalf("control session %+v %v", gotSess, err)
	}
	gotRun, err := control.GetRun(ctx, created.ID)
	if err != nil || gotRun.State != run.Created || gotRun.SessionID != sess.ID {
		t.Fatalf("control run %+v %v", gotRun, err)
	}
}

func deleteJobs(t *testing.T, dbURL, runID string) error {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	_, err = conn.Exec(context.Background(), `DELETE FROM run_jobs WHERE run_id=$1`, runID)
	return err
}

func jobStatus(t *testing.T, dbURL, runID string) (string, string) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var status, owner string
	if err := conn.QueryRow(context.Background(), `SELECT status, COALESCE(lease_owner, '') FROM run_jobs WHERE run_id=$1`, runID).Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	return status, owner
}
