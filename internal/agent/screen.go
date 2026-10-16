package agent

import (
	"fmt"
	"strings"

	"github.com/csy20/agt/internal/config"
)

// Entry is one row of the capability screen.
// A model-api row is not a coding-agent row.
type Entry struct {
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Adapter          string `json:"adapter,omitempty"`
	Model            string `json:"model,omitempty"`
	Endpoint         string `json:"endpoint,omitempty"`
	Scripted         bool   `json:"scripted"`
	Valid            bool   `json:"valid"`
	Problem          string `json:"problem,omitempty"`
	Protocol         string `json:"protocol,omitempty"`
	Installed        bool   `json:"installed,omitempty"`
	RuntimeVersion   string `json:"runtime_version,omitempty"`
	RuntimeSupported bool   `json:"runtime_supported,omitempty"`
	Authentication   string `json:"authentication,omitempty"`
	Execution        string `json:"execution"`
	Mutation         string `json:"mutation,omitempty"`
	LiveInference    bool   `json:"live_inference"`
	Reason           string `json:"reason,omitempty"`
}

// Screen separates model API adapters from external coding agents.
type Screen struct {
	Entries []Entry `json:"entries"`
	Notice  string  `json:"notice"`
}

const screenNotice = "No inference request was made. No coding agent was started. Scripted protocol fixtures are not a live Codex or Claude run."

// BuildScreen lists model configurations and coding-agent probes.
// live_inference is false on every row. This function does not call a model.
func BuildScreen(models []config.PublicModel, agents []CodingAgent) Screen {
	entries := make([]Entry, 0, len(models)+len(agents))
	for _, model := range models {
		execution := "not-called"
		if model.Scripted {
			execution = "scripted"
		}
		entries = append(entries, Entry{
			Kind:          KindModelAPI,
			Name:          model.Name,
			Adapter:       model.Adapter,
			Model:         model.Model,
			Endpoint:      model.Endpoint,
			Scripted:      model.Scripted,
			Valid:         model.Valid,
			Problem:       model.Problem,
			Execution:     execution,
			LiveInference: false,
		})
	}
	for _, item := range agents {
		entries = append(entries, Entry{
			Kind:             KindCodingAgent,
			Name:             item.Name,
			Protocol:         item.Protocol,
			Installed:        item.Installed,
			RuntimeVersion:   item.Version,
			RuntimeSupported: item.VersionSupported,
			Authentication:   item.Authentication,
			Execution:        "unavailable",
			Mutation:         "unavailable",
			Scripted:         true,
			LiveInference:    false,
			Reason:           item.Reason,
		})
	}
	return Screen{Entries: entries, Notice: screenNotice}
}

// FormatScreen prints the capability screen for the CLI.
func FormatScreen(screen Screen) string {
	var b strings.Builder
	if len(screen.Entries) == 0 {
		b.WriteString("no adapters\n")
	}
	for _, entry := range screen.Entries {
		if entry.Kind == KindModelAPI {
			kind := entry.Adapter
			if entry.Scripted {
				kind = "scripted"
			}
			fmt.Fprintf(&b, "model-api  %s  %s  model %s  execution %s\n", entry.Name, kind, empty(entry.Model), entry.Execution)
			if !entry.Valid && entry.Problem != "" {
				fmt.Fprintf(&b, "  invalid: %s\n", entry.Problem)
			}
			continue
		}
		fmt.Fprintf(&b, "coding-agent  %s  protocol %s  installed %t  runtime %s  execution %s  mutation %s\n",
			entry.Name, empty(entry.Protocol), entry.Installed, empty(entry.RuntimeVersion), entry.Execution, entry.Mutation)
		if entry.Reason != "" {
			fmt.Fprintf(&b, "  %s\n", entry.Reason)
		}
	}
	b.WriteString(screen.Notice)
	b.WriteByte('\n')
	return b.String()
}

func empty(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(unset)"
	}
	return v
}
