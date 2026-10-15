package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/csy20/agt/internal/tools"
)

const (
	maxModelBody     = 1 << 20
	defaultMaxOutput = 2048
	defaultAttempts  = 3
)

// OpenAI is a provider-neutral chat-completions client. Base URL, model name,
// and prices come from configuration. A loopback base URL may omit an API key.
type OpenAI struct {
	BaseURL         string
	APIKey          string
	Model           string
	System          string
	HTTP            *http.Client
	MaxOutputTokens int
	MaxAttempts     int
}

func NewOpenAI(baseURL, apiKey, modelName string) *OpenAI {
	baseURL = strings.TrimRight(baseURL, "/")
	return &OpenAI{
		BaseURL:         baseURL,
		APIKey:          apiKey,
		Model:           modelName,
		System:          SystemPrompt,
		HTTP:            &http.Client{Timeout: 90 * time.Second},
		MaxOutputTokens: defaultMaxOutput,
		MaxAttempts:     defaultAttempts,
	}
}

// LoopbackURL reports whether raw is an HTTP(S) URL on localhost or a loopback IP.
func LoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *OpenAI) validate() error {
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("openai provider requires AGT_MODEL_NAME")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("AGT_MODEL_BASE_URL must be an http or https URL")
	}
	if c.APIKey == "" && !LoopbackURL(c.BaseURL) {
		return fmt.Errorf("cloud model endpoints require AGT_MODEL_API_KEY")
	}
	return nil
}

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []chatMessage  `json:"messages"`
	Tools         []any          `json:"tools,omitempty"`
	ToolChoice    string         `json:"tool_choice,omitempty"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []outboundCall `json:"tool_calls,omitempty"`
}

type outboundCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type inboundCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string        `json:"content"`
			ToolCalls []inboundCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		Message struct {
			Content   string        `json:"content"`
			ToolCalls []inboundCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	if err := c.validate(); err != nil {
		return Response{}, err
	}
	messages, err := c.messages(req)
	if err != nil {
		return Response{}, err
	}
	full := chatRequest{
		Model:         c.Model,
		Messages:      messages,
		Tools:         tools.Definitions(),
		ToolChoice:    "auto",
		MaxTokens:     c.maxOutput(),
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	minimal := chatRequest{
		Model:      c.Model,
		Messages:   messages,
		Tools:      tools.Definitions(),
		ToolChoice: "auto",
	}
	attempts := c.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	useMinimal := false
	var last error
	posts := 0
	for try := 0; try < attempts; try++ {
		if err := ctx.Err(); err != nil {
			return Response{}, err
		}
		if try > 0 {
			if err := sleep(ctx, backoff(try)); err != nil {
				return Response{}, err
			}
		}
		payload := full
		if useMinimal {
			payload = minimal
		}
		var streamed bool
		onText := func(text string) {
			if text == "" || req.Display == nil {
				return
			}
			streamed = true
			req.Display(text)
		}
		status, raw, err := c.post(ctx, payload, onText)
		posts++
		if err != nil {
			if ctx.Err() != nil {
				return Response{}, ctx.Err()
			}
			last = err
			// A later attempt would append the same delta again. The partial
			// body is not a tool call, so it is not executed.
			if streamed {
				break
			}
			continue
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			last = fmt.Errorf("model HTTP %d after %d attempts: %s", status, try+1, clip(string(raw), 400))
			continue
		}
		if status == http.StatusBadRequest && !useMinimal && capabilityRejected(raw) {
			useMinimal = true
			try--
			continue
		}
		if status >= 300 {
			return Response{}, fmt.Errorf("model HTTP %d: %s", status, clip(string(raw), 400))
		}
		parsed, err := parseModelBody(raw)
		if err != nil {
			return Response{}, err
		}
		if !streamed && parsed.Text != "" && req.Display != nil {
			req.Display(parsed.Text)
		}
		parsed.ModelName = c.Model
		parsed.Attempts = posts
		return parsed, nil
	}
	if last == nil {
		last = fmt.Errorf("model request failed")
	}
	return Response{}, &StatusError{Attempts: posts, Model: c.Model, Err: last}
}

func (c *OpenAI) maxOutput() int {
	if c.MaxOutputTokens <= 0 {
		return defaultMaxOutput
	}
	return c.MaxOutputTokens
}

func (c *OpenAI) messages(req Request) ([]chatMessage, error) {
	var turns []Turn
	if len(req.Transcript) > 0 {
		turns = req.Transcript
	} else {
		for _, msg := range BuildMessages(c.System, req) {
			turns = append(turns, Turn{Role: msg.Role, Content: msg.Content})
		}
	}
	out := make([]chatMessage, 0, len(turns))
	for _, turn := range turns {
		msg := chatMessage{Role: turn.Role, Content: turn.Content, ToolCallID: turn.ToolCallID}
		if turn.Role == "assistant" && turn.ToolCallID != "" {
			args := turn.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			call := outboundCall{ID: turn.ToolCallID, Type: "function"}
			call.Function.Name = turn.ToolName
			call.Function.Arguments = args
			msg.ToolCalls = []outboundCall{call}
		}
		out = append(out, msg)
	}
	return out, nil
}

func (c *OpenAI) post(ctx context.Context, payload chatRequest, onText func(string)) (int, []byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if payload.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var raw []byte
	// Error bodies are not shown. Text is emitted only while a successful stream is read.
	if resp.StatusCode < 300 && payload.Stream {
		raw, err = readStream(resp.Body, maxModelBody, onText)
	} else {
		raw, err = readLimited(resp.Body, maxModelBody)
	}
	if err != nil {
		return resp.StatusCode, raw, err
	}
	return resp.StatusCode, raw, nil
}

// readStream keeps the 1 MiB cap and reports content deltas as they arrive.
// Tool-call fragments are left for parseSSE, which accepts one call only after the body is complete.
func readStream(r io.Reader, limit int, onText func(string)) ([]byte, error) {
	br := bufio.NewReader(io.LimitReader(r, int64(limit)+1))
	var raw bytes.Buffer
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			raw.Write(line)
			if raw.Len() > limit {
				return nil, fmt.Errorf("model response exceeded %d bytes", limit)
			}
			emitStreamText(string(line), onText)
		}
		if err == io.EOF {
			return raw.Bytes(), nil
		}
		if err != nil {
			return raw.Bytes(), err
		}
	}
}

func emitStreamText(line string, onText func(string)) {
	if onText == nil {
		return
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" || data[0] != '{' {
		return
	}
	var chunk streamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return
	}
	if len(chunk.Choices) == 0 {
		return
	}
	choice := chunk.Choices[0]
	text := choice.Delta.Content
	if text == "" {
		text = choice.Message.Content
	}
	if text != "" {
		onText(text)
	}
}

func readLimited(r io.Reader, limit int) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("model response exceeded %d bytes", limit)
	}
	return raw, nil
}

func capabilityRejected(raw []byte) bool {
	text := strings.ToLower(string(raw))
	for _, word := range []string{"stream", "max_tokens", "stream_options", "include_usage"} {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

func parseModelBody(raw []byte) (Response, error) {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 {
		return Response{}, fmt.Errorf("model returned an empty body")
	}
	if trim[0] == '{' {
		return parseJSON(trim)
	}
	return parseSSE(trim)
}

func parseJSON(raw []byte) (Response, error) {
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Response{}, err
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Response{}, fmt.Errorf("model error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("model returned no choices")
	}
	msg := parsed.Choices[0].Message
	return responseFrom(msg.Content, msg.ToolCalls, parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens)
}

func parseSSE(raw []byte) (Response, error) {
	var content strings.Builder
	calls := map[int]*inboundCall{}
	var order []int
	input, output := 0, 0
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			return Response{}, fmt.Errorf("model stream line is not an event")
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Response{}, fmt.Errorf("model stream JSON: %w", err)
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return Response{}, fmt.Errorf("model error: %s", chunk.Error.Message)
		}
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			input = chunk.Usage.PromptTokens
			output = chunk.Usage.CompletionTokens
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if len(choice.Message.ToolCalls) > 0 || choice.Message.Content != "" {
			return responseFrom(choice.Message.Content, choice.Message.ToolCalls, input, output)
		}
		content.WriteString(choice.Delta.Content)
		for _, delta := range choice.Delta.ToolCalls {
			call := calls[delta.Index]
			if call == nil {
				call = &inboundCall{}
				calls[delta.Index] = call
				order = append(order, delta.Index)
			}
			if delta.ID != "" {
				call.ID = delta.ID
			}
			if delta.Function.Name != "" {
				call.Function.Name += delta.Function.Name
			}
			call.Function.Arguments += delta.Function.Arguments
		}
	}
	assembled := make([]inboundCall, 0, len(order))
	for _, index := range order {
		assembled = append(assembled, *calls[index])
	}
	if content.Len() == 0 && len(assembled) == 0 {
		return Response{}, fmt.Errorf("model returned no choices")
	}
	return responseFrom(content.String(), assembled, input, output)
}

func responseFrom(content string, calls []inboundCall, input, output int) (Response, error) {
	if len(calls) > 1 {
		return Response{}, fmt.Errorf("model returned %d tool calls; one call is required: %s", len(calls), describeCalls(calls))
	}
	out := Response{Text: content, InputTokens: input, OutputTokens: output}
	if len(calls) == 1 {
		call := calls[0]
		if strings.TrimSpace(call.Function.Name) == "" {
			return Response{}, fmt.Errorf("model tool call is missing a name")
		}
		args := strings.TrimSpace(call.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return Response{}, fmt.Errorf("model tool arguments are not JSON")
		}
		id := call.ID
		if id == "" {
			id = "call_1"
		}
		out.Tool = call.Function.Name
		out.Args = json.RawMessage(args)
		out.ToolCallID = id
		return out, nil
	}
	if plan := planFromContent(content); len(plan) > 0 {
		out.Plan = plan
		out.Text = content
		return out, nil
	}
	out.Finish = true
	if out.Text == "" {
		out.Text = "finished"
	}
	return out, nil
}

func planFromContent(content string) json.RawMessage {
	trim := strings.TrimSpace(content)
	if !strings.HasPrefix(trim, "{") {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trim), &obj); err != nil {
		return nil
	}
	if _, ok := obj["goal"]; !ok {
		return nil
	}
	if _, ok := obj["steps"]; !ok {
		return nil
	}
	return json.RawMessage(trim)
}

func describeCalls(calls []inboundCall) string {
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		id := call.ID
		if id == "" {
			id = "?"
		}
		name := call.Function.Name
		if name == "" {
			name = "?"
		}
		parts = append(parts, id+":"+name)
	}
	return strings.Join(parts, ", ")
}

func backoff(try int) time.Duration {
	d := time.Duration(try) * 20 * time.Millisecond
	if d > time.Second {
		return time.Second
	}
	return d
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
