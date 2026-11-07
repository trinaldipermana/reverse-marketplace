package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
)

var (
	ErrRefusal    = errors.New("OpenAI refused the request")
	ErrIncomplete = errors.New("OpenAI response was not completed")
)

// APIError deliberately omits upstream error bodies, which can echo private input.
type APIError struct{ StatusCode int }

func (e *APIError) Error() string { return fmt.Sprintf("OpenAI returned HTTP %d", e.StatusCode) }

type OpenAIClient struct {
	config     Config
	httpClient *http.Client
}

var _ LLM = (*OpenAIClient)(nil)

func NewOpenAIClient(cfg Config) (*OpenAIClient, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &OpenAIClient{config: cfg, httpClient: &http.Client{
		// Do not forward credentials or input through unexpected redirects.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type functionTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type apiRequest struct {
	Model             string            `json:"model"`
	Input             []json.RawMessage `json:"input"`
	Tools             []functionTool    `json:"tools,omitempty"`
	ParallelToolCalls bool              `json:"parallel_tool_calls"`
	Store             bool              `json:"store"`
	Include           []string          `json:"include"`
	Text              any               `json:"text,omitempty"`
}

type apiResponse struct {
	Status string            `json:"status"`
	Output []json.RawMessage `json:"output"`
}

func (c *OpenAIClient) Generate(ctx context.Context, req Request) (Response, error) {
	input := []json.RawMessage{}
	if req.Continuation != nil {
		input = append(input, req.Continuation.input...)
	}
	for _, result := range req.ToolResults {
		if result.CallID == "" {
			return Response{}, errors.New("tool result requires a call ID")
		}
		raw, _ := json.Marshal(map[string]string{"type": "function_call_output", "call_id": result.CallID, "output": result.Output})
		input = append(input, raw)
	}
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system", "developer", "user", "assistant":
		default:
			return Response{}, errors.New("unsupported message role")
		}
		raw, _ := json.Marshal(msg)
		input = append(input, raw)
	}
	if len(input) == 0 {
		return Response{}, errors.New("at least one input message is required")
	}
	body := c.request(input)
	for _, tool := range req.Tools {
		if tool.Name == "" || !json.Valid(tool.Schema) {
			return Response{}, errors.New("tool requires a name and valid JSON schema")
		}
		body.Tools = append(body.Tools, functionTool{Type: "function", Name: tool.Name, Description: tool.Description, Parameters: tool.Schema, Strict: true})
	}
	out, err := c.call(ctx, body)
	if err != nil {
		return Response{}, err
	}
	response, err := decodeOutput(out)
	if err != nil {
		return Response{}, err
	}
	response.Continuation = &Continuation{input: append(input, out.Output...)}
	return response, nil
}

// GenerateJSON asks OpenAI for strict schema output and decodes into a non-nil
// pointer. The caller owns the schema and any business validation.
func (c *OpenAIClient) GenerateJSON(ctx context.Context, req JSONRequest, out any) error {
	value := reflect.ValueOf(out)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return errors.New("JSON output must be a non-nil pointer")
	}
	if req.SchemaName == "" || !json.Valid(req.Schema) {
		return errors.New("structured output requires a schema name and valid JSON schema")
	}
	input := []json.RawMessage{}
	if req.Instructions != "" {
		msg, _ := json.Marshal(ChatMessage{Role: "developer", Content: req.Instructions})
		input = append(input, msg)
	}
	msg, _ := json.Marshal(ChatMessage{Role: "user", Content: req.Input})
	input = append(input, msg)
	body := c.request(input)
	body.Text = map[string]any{"format": map[string]any{"type": "json_schema", "name": req.SchemaName, "schema": req.Schema, "strict": true}}
	result, err := c.call(ctx, body)
	if err != nil {
		return err
	}
	response, err := decodeOutput(result)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(response.Text), out); err != nil {
		return errors.New("OpenAI returned invalid structured output")
	}
	return nil
}

func (c *OpenAIClient) request(input []json.RawMessage) apiRequest {
	return apiRequest{Model: c.config.Model, Input: input, Include: []string{"reasoning.encrypted_content"}}
}

func (c *OpenAIClient) call(ctx context.Context, body apiRequest) (apiResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.RequestTimeout)
	defer cancel()
	encoded, err := json.Marshal(body)
	if err != nil {
		return apiResponse{}, errors.New("could not encode OpenAI request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(encoded))
	if err != nil {
		return apiResponse{}, errors.New("could not construct OpenAI request")
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return apiResponse{}, fmt.Errorf("OpenAI request: %w", ctx.Err())
		}
		return apiResponse{}, errors.New("OpenAI request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiResponse{}, &APIError{StatusCode: res.StatusCode}
	}
	const maxBody = 4 << 20
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		if ctx.Err() != nil {
			return apiResponse{}, fmt.Errorf("OpenAI response: %w", ctx.Err())
		}
		return apiResponse{}, errors.New("could not read OpenAI response")
	}
	if len(data) > maxBody {
		return apiResponse{}, errors.New("OpenAI response exceeded size limit")
	}
	var result apiResponse
	if json.Unmarshal(data, &result) != nil {
		return apiResponse{}, errors.New("could not decode OpenAI response")
	}
	if result.Status != "completed" {
		return apiResponse{}, ErrIncomplete
	}
	return result, nil
}

func decodeOutput(out apiResponse) (Response, error) {
	var result Response
	var text strings.Builder
	for _, raw := range out.Output {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return Response{}, errors.New("could not decode OpenAI output item")
		}
		switch item.Type {
		case "function_call":
			if item.CallID == "" || item.Name == "" || !json.Valid([]byte(item.Arguments)) {
				return Response{}, errors.New("OpenAI returned an invalid tool call")
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: item.CallID, Name: item.Name, Args: json.RawMessage(item.Arguments)})
		case "message":
			for _, content := range item.Content {
				if content.Type == "refusal" {
					return Response{}, ErrRefusal
				}
				if content.Type == "output_text" {
					text.WriteString(content.Text)
				}
			}
		}
	}
	result.Text = text.String()
	if result.Text == "" && len(result.ToolCalls) == 0 {
		return Response{}, errors.New("OpenAI returned no text or tool calls")
	}
	return result, nil
}
