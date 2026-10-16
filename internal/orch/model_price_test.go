package orch_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/orch"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
)

func TestRecordedPricesStayWithTheRun(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	eng.ModelName = "engine-model"
	prof := eng.MustProfile(t, testutil.PayrepoRequest())
	raw, err := prof.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	script := []byte(`{"steps":[{"finish":true,"text":"priced answer","input_tokens":11,"output_tokens":7}]}`)
	created, _, err := st.CreateRun(context.Background(), store.CreateInput{
		Goal:             "what does charge do?",
		RepoPath:         testutil.Fixture(t),
		BudgetCents:      100,
		MaxSteps:         4,
		WallSeconds:      300,
		Provider:         "fake",
		RequestHash:      id.New("req_"),
		ScriptJSON:       script,
		ProfileJSON:      raw,
		Interaction:      "ask",
		ModelConfig:      "priced",
		ModelName:        "priced-model",
		PricingRecorded:  true,
		InputPerMillion:  3_000_000,
		OutputPerMillion: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := eng.Orch.Run(ctx, created.ID); err != nil {
		testutil.Dump(t, st, created.ID)
		t.Fatal(err)
	}
	current, err := st.GetRun(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != run.Succeeded || current.ModelName != "priced-model" || !current.PricingRecorded {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("run state %s model %s recorded %v", current.State, current.ModelName, current.PricingRecorded)
	}
	usage, err := st.ListUsage(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, row := range usage {
		if row.Kind != "model.call" {
			continue
		}
		saw = true
		if row.CostMicros != 33 || row.ModelName != "priced-model" {
			t.Fatalf("usage %+v", row)
		}
	}
	if !saw {
		t.Fatal("no model.call usage")
	}
	events, err := st.EventsAfter(ctx, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type != "model.called" {
			continue
		}
		var payload struct {
			CostMicros int64  `json:"cost_micros"`
			ModelName  string `json:"model_name"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.CostMicros != 33 || payload.ModelName != "priced-model" {
			t.Fatalf("event %+v", payload)
		}
		return
	}
	t.Fatal("model.called was not recorded")
}
