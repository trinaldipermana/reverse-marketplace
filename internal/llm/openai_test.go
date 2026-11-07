package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testClient(t *testing.T, handle func(*http.Request) string) *OpenAIClient {
	t.Helper()
	c, err := NewOpenAIClient(Config{APIKey: "test-key", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	c.httpClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(handle(r))), Header: make(http.Header)}, nil
	})}
	return c
}

func TestGenerateToolRoundTrip(t *testing.T) {
	var requests []map[string]any
	c := testClient(t, func(r *http.Request) string {
		if r.Method != "POST" || r.URL.String() != "https://api.openai.com/v1/responses" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("incorrect API request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, body)
		if len(requests) == 1 {
			return `{"status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_item","arguments":"{\"item_id\":\"item-1\"}"}]}`
		}
		return `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Item found."}]}]}`
	})
	tools := []ToolDefinition{{Name: "get_item", Description: "Read an item", Schema: json.RawMessage(`{"type":"object","properties":{"item_id":{"type":"string"}},"required":["item_id"],"additionalProperties":false}`)}}
	r, err := c.Generate(context.Background(), Request{Messages: []ChatMessage{{Role: "user", Content: "Find item-1"}}, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].ID != "call_1" || r.ToolCalls[0].Name != "get_item" || string(r.ToolCalls[0].Args) != `{"item_id":"item-1"}` {
		t.Fatalf("incorrect tool call: %+v", r.ToolCalls)
	}
	result, err := c.Generate(context.Background(), Request{Continuation: r.Continuation, ToolResults: []ToolResult{{CallID: "call_1", Output: `{"stock":1}`}}, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Item found." {
		t.Fatalf("text = %q", result.Text)
	}
	if requests[0]["store"] != false || requests[0]["parallel_tool_calls"] != false || requests[0]["model"] != "test-model" {
		t.Fatal("unsafe or incorrect request settings")
	}
	input := requests[1]["input"].([]any)
	if len(input) != 4 || input[1].(map[string]any)["type"] != "reasoning" || input[3].(map[string]any)["call_id"] != "call_1" {
		t.Fatal("continuation lost reasoning or tool-call identity")
	}
}

func TestGenerateJSON(t *testing.T) {
	c := testClient(t, func(r *http.Request) string {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		format := body["text"].(map[string]any)["format"].(map[string]any)
		if format["type"] != "json_schema" || format["strict"] != true {
			t.Fatal("missing strict JSON output")
		}
		return `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"is_request\":true}"}]}]}`
	})
	var out struct {
		IsRequest bool `json:"is_request"`
	}
	err := c.GenerateJSON(context.Background(), JSONRequest{Input: "Looking for a bike", SchemaName: "intent", Schema: json.RawMessage(`{"type":"object","properties":{"is_request":{"type":"boolean"}},"required":["is_request"],"additionalProperties":false}`)}, &out)
	if err != nil || !out.IsRequest {
		t.Fatalf("output = %+v, error = %v", out, err)
	}
}

func TestConfiguration(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("missing key accepted")
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_MODEL", "")
	cfg, err := ConfigFromEnv()
	if err != nil || cfg.Model != "gpt-6.1-sol" {
		t.Fatalf("incorrect default model, error = %v", err)
	}
	t.Setenv("OPENAI_MODEL", "another-model")
	cfg, err = ConfigFromEnv()
	if err != nil || cfg.Model != "another-model" {
		t.Fatal("model override failed")
	}
}

func TestFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"refusal", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"private content"}]}]}`, 200, ErrRefusal},
		{"incomplete", `{"status":"incomplete","output":[]}`, 200, ErrIncomplete},
		{"api error", `{"error":{"message":"test-key private content"}}`, 401, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(*http.Request) string { return tc.body })
			base := c.httpClient.Transport
			c.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				res, err := base.RoundTrip(r)
				res.StatusCode = tc.status
				return res, err
			})
			_, err := c.Generate(context.Background(), Request{Messages: []ChatMessage{{Role: "user", Content: "Hello"}}})
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), "test-key") || strings.Contains(err.Error(), "private content") {
				t.Fatal("error leaked sensitive content")
			}
		})
	}
}

func TestGenerateJSONRejectsInvalidInputBeforeRequest(t *testing.T) {
	valid := JSONRequest{SchemaName: "intent", Schema: json.RawMessage(`{"type":"object"}`)}
	var typedNil *struct{}
	for _, tc := range []struct {
		name    string
		request JSONRequest
		out     any
	}{
		{"nil output", valid, nil},
		{"non-pointer output", valid, struct{}{}},
		{"typed nil output", valid, typedNil},
		{"missing schema name", JSONRequest{Schema: valid.Schema}, &struct{}{}},
		{"invalid schema JSON", JSONRequest{SchemaName: "intent", Schema: json.RawMessage(`{`)}, &struct{}{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(*http.Request) string {
				t.Fatal("invalid local input reached provider")
				return ""
			})
			if err := c.GenerateJSON(context.Background(), tc.request, tc.out); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestGenerateJSONReturnsGenerationFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{"refusal", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"private content"}]}]}`, ErrRefusal},
		{"incomplete", `{"status":"incomplete","output":[]}`, ErrIncomplete},
		{"invalid JSON answer", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"not JSON"}]}]}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(*http.Request) string { return tc.body })
			var out struct {
				IsRequest bool `json:"is_request"`
			}
			err := c.GenerateJSON(context.Background(), JSONRequest{SchemaName: "intent", Schema: json.RawMessage(`{"type":"object"}`)}, &out)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), "private content") {
				t.Fatal("error exposed model content")
			}
		})
	}
}
