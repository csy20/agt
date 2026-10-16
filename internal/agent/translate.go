package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/csy20/agt/internal/policy"
	"github.com/csy20/agt/internal/tools"
)

// translateClaudeTool maps one documented tool_use block through ask-mode policy.
// The read is not performed. Edit, Write, and Bash are denied. A subagent call
// is denied even when the same Read would be allowed for the parent, because a
// parent permission callback does not intercept every Claude call.
func translateClaudeTool(block []byte, subagent bool) (ToolRequest, error) {
	var tool struct {
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(block, &tool); err != nil {
		return ToolRequest{}, protocolf("tool_use")
	}
	if strings.TrimSpace(tool.Name) == "" {
		return ToolRequest{}, protocolf("tool_use requires name")
	}
	input := tool.Input
	if len(bytes.TrimSpace(input)) == 0 {
		input = []byte(`{}`)
	}
	req, err := translateNamed(tool.ID, tool.Name, input)
	if err != nil {
		return ToolRequest{}, err
	}
	if subagent {
		req.Subagent = true
		req.Mediated = false
		req.Decision = string(policy.Deny)
		req.Reason = "subagent tool call is outside the parent permission callback and was not executed"
	}
	req.Executed = false
	return req, nil
}

func translateNamed(id, name string, input json.RawMessage) (ToolRequest, error) {
	switch name {
	case "Read":
		return translateRead(id, input)
	case "Grep":
		return translateGrep(id, input)
	case "Edit":
		return translateEdit(id, input)
	case "Write":
		return translateWrite(id, input)
	case "Bash", "Glob", "NotebookEdit", "MultiEdit":
		return denyUnmediated(id, name, input, name+" is not an AGT read tool and was not executed"), nil
	default:
		return denyUnmediated(id, name, input, "unrecognised external tool was not executed"), nil
	}
}

func translateRead(id string, input json.RawMessage) (ToolRequest, error) {
	var args struct {
		FilePath string `json:"file_path"`
		Offset   *int   `json:"offset"`
		Limit    *int   `json:"limit"`
	}
	if err := strict(input, &args); err != nil {
		return denyUnmediated(id, "Read", input, "Read arguments are not the pinned file_path window"), nil
	}
	if args.Offset != nil || args.Limit != nil {
		return denyUnmediated(id, "Read", input, "Read offset and limit are not pinned to an AGT line window"), nil
	}
	if strings.TrimSpace(args.FilePath) == "" {
		return denyUnmediated(id, "Read", input, "Read requires file_path"), nil
	}
	body := tools.ReadArgs{Path: args.FilePath, Start: 1, End: 400}
	return mediate(id, "Read", input, "read_file_range", body, "AGT read is lines 1-400; the file was not read")
}

func translateGrep(id string, input json.RawMessage) (ToolRequest, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Glob    string `json:"glob"`
	}
	if err := strict(input, &args); err != nil {
		return denyUnmediated(id, "Grep", input, "Grep arguments are not the pinned pattern"), nil
	}
	if strings.TrimSpace(args.Path) != "" && args.Path != "." {
		return denyUnmediated(id, "Grep", input, "Grep path is not an AGT search"), nil
	}
	if strings.TrimSpace(args.Glob) != "" {
		return denyUnmediated(id, "Grep", input, "Grep glob is not an AGT search"), nil
	}
	return mediate(id, "Grep", input, "search_code", tools.SearchArgs{Query: args.Pattern}, "search was not executed")
}

func translateEdit(id string, input json.RawMessage) (ToolRequest, error) {
	var args struct {
		FilePath string `json:"file_path"`
		Old      string `json:"old_string"`
		New      string `json:"new_string"`
	}
	if err := strict(input, &args); err != nil {
		return denyUnmediated(id, "Edit", input, "Edit arguments are not the pinned path, old_string, and new_string"), nil
	}
	body := tools.PatchArgs{Path: args.FilePath, Old: args.Old, New: args.New}
	return mediate(id, "Edit", input, "apply_patch", body, "")
}

func translateWrite(id string, input json.RawMessage) (ToolRequest, error) {
	var args struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := strict(input, &args); err != nil {
		return denyUnmediated(id, "Write", input, "Write arguments are not the pinned path and content"), nil
	}
	body := tools.WriteArgs{Path: args.FilePath, Content: args.Content}
	return mediate(id, "Write", input, "write_file", body, "")
}

func mediate(id, providerTool string, providerArgs json.RawMessage, agtTool string, agtArgs any, note string) (ToolRequest, error) {
	body, err := json.Marshal(agtArgs)
	if err != nil {
		return ToolRequest{}, err
	}
	// Ask mode is the read-only boundary. Agent mode is not used here.
	decision, err := policy.DecideFor("ask", agtTool, body)
	if err != nil {
		return ToolRequest{}, err
	}
	reason := decision.Reason
	if note != "" && decision.Decision == policy.Allow {
		reason = decision.Reason + "; " + note
	}
	return ToolRequest{
		CallID:       id,
		ProviderTool: providerTool,
		ProviderArgs: append([]byte(nil), providerArgs...),
		AGTTool:      agtTool,
		AGTArgs:      body,
		ArgsHash:     decision.ArgsHash,
		Decision:     string(decision.Decision),
		Mediated:     true,
		Executed:     false,
		Reason:       reason,
	}, nil
}

func denyUnmediated(id, providerTool string, raw json.RawMessage, reason string) ToolRequest {
	return ToolRequest{
		CallID:       id,
		ProviderTool: providerTool,
		ProviderArgs: append([]byte(nil), raw...),
		Decision:     string(policy.Deny),
		Mediated:     false,
		Executed:     false,
		Reason:       reason,
	}
}

func strict(raw json.RawMessage, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing json")
	}
	return nil
}
