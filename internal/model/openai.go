package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/csy20/agt/internal/tools"
)

// OpenAI is a provider-neutral chat-completions client. Base URL, model name,
// and prices come from configuration.
type OpenAI struct {
	BaseURL string
	APIKey  string
	Model   string
	System  string
	HTTP    *http.Client
}

func NewOpenAI(baseURL, apiKey, modelName string) *OpenAI {
	baseURL = strings.TrimRight(baseURL, "/")
	return &OpenAI{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   modelName,
		System:  SystemPrompt,
		HTTP:    &http.Client{Timeout: 90 * time.Second},
	}
}

type chatRequest struct {
	Model      string        `json:"model"`
	Messages   []chatMessage `json:"messages"`
	Tools      []any         `json:"tools,omitempty"`
	ToolChoice string        `json:"tool_choice,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
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
	if c.APIKey == "" || c.Model == "" {
		return Response{}, fmt.Errorf("openai provider requires an API key and model name")
	}
	msgs := BuildMessages(c.System, req)
	payload := chatRequest{
		Model:      c.Model,
		Tools:      tools.Definitions(),
		ToolChoice: "auto",
	}
	for _, msg := range msgs {
		payload.Messages = append(payload.Messages, chatMessage{Role: msg.Role, Content: msg.Content})
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Response{}, err
	}
	if resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("model HTTP %d: %s", resp.StatusCode, clip(string(raw), 400))
	}
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
	out := Response{
		Text:         msg.Content,
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}
	if len(msg.ToolCalls) > 0 {
		call := msg.ToolCalls[0].Function
		out.Tool = call.Name
		args := strings.TrimSpace(call.Arguments)
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return Response{}, fmt.Errorf("model tool arguments are not JSON")
		}
		out.Args = json.RawMessage(args)
		return out, nil
	}
	out.Finish = true
	if out.Text == "" {
		out.Text = "finished"
	}
	return out, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
