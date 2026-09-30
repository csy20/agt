package model

import (
	"context"
	"encoding/json"
	"fmt"
)

type Script struct {
	Steps []Step `json:"steps"`
}

type Step struct {
	Plan         json.RawMessage `json:"plan,omitempty"`
	Tool         string          `json:"tool,omitempty"`
	Args         json.RawMessage `json:"args,omitempty"`
	Finish       bool            `json:"finish,omitempty"`
	Text         string          `json:"text,omitempty"`
	InputTokens  int             `json:"input_tokens,omitempty"`
	OutputTokens int             `json:"output_tokens,omitempty"`
}

func ParseScript(raw []byte) (Script, error) {
	var script Script
	if err := json.Unmarshal(raw, &script); err != nil {
		return Script{}, err
	}
	if len(script.Steps) == 0 {
		return Script{}, fmt.Errorf("script has no steps")
	}
	for i, step := range script.Steps {
		if step.Finish && step.Tool != "" {
			return Script{}, fmt.Errorf("step %d sets both finish and tool", i)
		}
		if !step.Finish && step.Tool == "" && len(step.Plan) == 0 {
			return Script{}, fmt.Errorf("step %d has no action", i)
		}
		if step.InputTokens == 0 {
			script.Steps[i].InputTokens = 200
		}
		if step.OutputTokens == 0 {
			script.Steps[i].OutputTokens = 40
		}
	}
	return script, nil
}

type Fake struct {
	Script Script
}

func NewFake(script Script) *Fake {
	return &Fake{Script: script}
}

func (f *Fake) Complete(_ context.Context, req Request) (Response, error) {
	if req.CompletedCalls < 0 || req.CompletedCalls >= len(f.Script.Steps) {
		return Response{}, fmt.Errorf("fake provider has no step %d", req.CompletedCalls)
	}
	step := f.Script.Steps[req.CompletedCalls]
	resp := Response{
		Text:         step.Text,
		Plan:         step.Plan,
		Tool:         step.Tool,
		Args:         step.Args,
		Finish:       step.Finish,
		InputTokens:  step.InputTokens,
		OutputTokens: step.OutputTokens,
	}
	if resp.Finish && resp.Text == "" {
		resp.Text = "finished"
	}
	return resp, nil
}
