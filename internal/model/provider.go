package model

import (
	"context"
	"encoding/json"
)

// Observation is untrusted tool output. It is never copied into the system prompt.
type Observation struct {
	Tool   string
	Origin string
	Exit   int
	Output string
}

type Request struct {
	Goal           string
	System         string
	Plan           json.RawMessage
	Observations   []Observation
	CompletedCalls int
}

type Response struct {
	Text         string
	Plan         json.RawMessage
	Tool         string
	Args         json.RawMessage
	Finish       bool
	InputTokens  int
	OutputTokens int
}

type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

type Message struct {
	Role    string
	Content string
}

// BuildMessages keeps authority text in the system message and tags observations as data.
func BuildMessages(system string, req Request) []Message {
	if system == "" {
		system = SystemPrompt
	}
	msgs := []Message{{Role: "system", Content: system}}
	user := "GOAL\n" + req.Goal
	if len(req.Plan) > 0 && string(req.Plan) != "null" {
		user += "\n\nPLAN\n" + string(req.Plan)
	}
	msgs = append(msgs, Message{Role: "user", Content: user})
	for _, ob := range req.Observations {
		origin := ob.Origin
		if origin == "" {
			origin = "tool:" + ob.Tool
		}
		body := "DATA origin=" + origin + "\n"
		body += "exit=" + itoa(ob.Exit) + "\n"
		body += ob.Output
		msgs = append(msgs, Message{Role: "user", Content: body})
	}
	return msgs
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
