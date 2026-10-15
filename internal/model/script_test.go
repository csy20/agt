package model

import (
	"context"
	"testing"
)

func TestFakeRecordsConfiguredModelName(t *testing.T) {
	script, err := ParseScript([]byte(`{"steps":[{"finish":true,"text":"done","input_tokens":3,"output_tokens":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	named := NewFake(script)
	named.ModelName = "local-repair"
	resp, err := named.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ModelName != "local-repair" || !resp.Finish || resp.Text != "done" {
		t.Fatalf("%+v", resp)
	}
	bare, err := NewFake(script).Complete(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if bare.ModelName != "" {
		t.Fatalf("empty identity became %q", bare.ModelName)
	}
}
