package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
)

type loopModelFunc func(context.Context, llm.Request) (llm.Response, error)

func (f loopModelFunc) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	return f(ctx, req)
}

type loopTestTool struct {
	name string
	run  func(context.Context, json.RawMessage) (ToolOutcome, error)
}

func (t loopTestTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: t.name, Schema: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)}
}

func (t loopTestTool) Run(ctx context.Context, args json.RawMessage) (ToolOutcome, error) {
	return t.run(ctx, args)
}

func TestToolLoopPassesResultsAndContinuation(t *testing.T) {
	continuation := &llm.Continuation{}
	rounds, calls := 0, 0
	model := loopModelFunc(func(_ context.Context, req llm.Request) (llm.Response, error) {
		rounds++
		if len(req.Tools) != 1 || req.Tools[0].Name != "get_item" {
			t.Fatal("registered tool definition not provided")
		}
		if rounds == 1 {
			if len(req.Messages) != 1 {
				t.Fatal("initial message lost")
			}
			return llm.Response{ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "get_item", Args: json.RawMessage(`{}`)}}, Continuation: continuation}, nil
		}
		if req.Continuation != continuation || len(req.Messages) != 0 || len(req.ToolResults) != 1 || req.ToolResults[0].CallID != "call-1" || req.ToolResults[0].Output != `{"stock":1}` {
			t.Fatalf("incorrect continuation request: %+v", req)
		}
		return llm.Response{Text: "Item found"}, nil
	})
	tool := loopTestTool{name: "get_item", run: func(context.Context, json.RawMessage) (ToolOutcome, error) {
		calls++
		return ToolOutcome{Output: `{"stock":1}`}, nil
	}}
	loop, err := NewToolLoop(model, []Tool{tool}, LoopLimits{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), llm.Request{Messages: []llm.ChatMessage{{Role: "user", Content: "Find an item"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Text != "Item found" || result.Rounds != 2 || result.ToolCalls != 1 || result.Stopped || calls != 1 {
		t.Fatalf("incorrect result: %+v, calls=%d", result, calls)
	}
}

func TestToolLoopRejectsBadCallsWithinCaps(t *testing.T) {
	for _, tc := range []struct{ name, toolName, args, code string }{
		{"unknown", "missing", `{}`, "unknown_tool"},
		{"invalid JSON", "get_item", `{`, "invalid_arguments"},
		{"non-object JSON", "get_item", `null`, "invalid_arguments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rounds, calls := 0, 0
			model := loopModelFunc(func(_ context.Context, req llm.Request) (llm.Response, error) {
				rounds++
				if rounds == 2 {
					var problem struct {
						Error string `json:"error"`
					}
					if len(req.ToolResults) != 1 || json.Unmarshal([]byte(req.ToolResults[0].Output), &problem) != nil || problem.Error != tc.code {
						t.Fatalf("missing controlled error result: %+v", req.ToolResults)
					}
				}
				return llm.Response{ToolCalls: []llm.ToolCall{{ID: "call", Name: tc.toolName, Args: json.RawMessage(tc.args)}}, Continuation: &llm.Continuation{}}, nil
			})
			tool := loopTestTool{name: "get_item", run: func(context.Context, json.RawMessage) (ToolOutcome, error) { calls++; return ToolOutcome{}, nil }}
			loop, err := NewToolLoop(model, []Tool{tool}, LoopLimits{MaxToolCalls: 1})
			if err != nil {
				t.Fatal(err)
			}
			result, err := loop.Run(context.Background(), llm.Request{Messages: []llm.ChatMessage{{Role: "user", Content: "Start"}}})
			if !errors.Is(err, ErrToolCallLimit) || result.ToolCalls != 1 || rounds != 2 || calls != 0 {
				t.Fatalf("err=%v, result=%+v, model rounds=%d, executions=%d", err, result, rounds, calls)
			}
		})
	}
}

func TestToolLoopStopsImmediately(t *testing.T) {
	calls := 0
	model := loopModelFunc(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{ToolCalls: []llm.ToolCall{
			{ID: "stop", Name: "send_message", Args: json.RawMessage(`{}`)},
			{ID: "extra", Name: "send_message", Args: json.RawMessage(`{}`)},
		}, Continuation: &llm.Continuation{}}, nil
	})
	tool := loopTestTool{name: "send_message", run: func(context.Context, json.RawMessage) (ToolOutcome, error) {
		calls++
		return ToolOutcome{Output: "sent", Stop: true}, nil
	}}
	loop, err := NewToolLoop(model, []Tool{tool}, LoopLimits{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), llm.Request{Messages: []llm.ChatMessage{{Role: "user", Content: "Start"}}})
	if err != nil || !result.Stopped || result.ToolCalls != 1 || calls != 1 {
		t.Fatalf("err=%v, result=%+v, executions=%d", err, result, calls)
	}
}

func TestToolLoopRejectsProtocolBeforeSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name         string
		continuation *llm.Continuation
		ids          []string
	}{
		{"missing continuation", nil, []string{"first"}},
		{"missing later ID", &llm.Continuation{}, []string{"first", ""}},
		{"duplicate IDs", &llm.Continuation{}, []string{"first", "first"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			model := loopModelFunc(func(context.Context, llm.Request) (llm.Response, error) {
				response := llm.Response{Continuation: tc.continuation}
				for _, id := range tc.ids {
					response.ToolCalls = append(response.ToolCalls, llm.ToolCall{ID: id, Name: "send_message", Args: json.RawMessage(`{}`)})
				}
				return response, nil
			})
			tool := loopTestTool{name: "send_message", run: func(context.Context, json.RawMessage) (ToolOutcome, error) {
				calls++
				return ToolOutcome{Output: "sent", Stop: true}, nil
			}}
			loop, err := NewToolLoop(model, []Tool{tool}, LoopLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loop.Run(context.Background(), llm.Request{}); err == nil || calls != 0 {
				t.Fatalf("error=%v, executions=%d", err, calls)
			}
		})
	}
}

func TestToolLoopRejectsDuplicatesAndHonorsContext(t *testing.T) {
	model := loopModelFunc(func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	})
	tool := loopTestTool{name: "get_item"}
	if _, err := NewToolLoop(model, []Tool{tool, tool}, LoopLimits{}); err == nil {
		t.Fatal("duplicate tool accepted")
	}
	loop, err := NewToolLoop(model, nil, LoopLimits{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loop.Run(ctx, llm.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestToolLoopRoundLimitAndToolFailure(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "round limit", true: "tool failure"}[fails], func(t *testing.T) {
			calls := 0
			failure := errors.New("dependency unavailable")
			model := loopModelFunc(func(context.Context, llm.Request) (llm.Response, error) {
				return llm.Response{ToolCalls: []llm.ToolCall{{ID: "call", Name: "get_item", Args: json.RawMessage(`{}`)}}, Continuation: &llm.Continuation{}}, nil
			})
			tool := loopTestTool{name: "get_item", run: func(context.Context, json.RawMessage) (ToolOutcome, error) {
				calls++
				if fails {
					return ToolOutcome{}, failure
				}
				return ToolOutcome{Output: "found"}, nil
			}}
			loop, err := NewToolLoop(model, []Tool{tool}, LoopLimits{MaxRounds: 2})
			if err != nil {
				t.Fatal(err)
			}
			result, err := loop.Run(context.Background(), llm.Request{Messages: []llm.ChatMessage{{Role: "user", Content: "Start"}}})
			if fails {
				if !errors.Is(err, failure) || calls != 1 || result.Rounds != 1 {
					t.Fatalf("error=%v, result=%+v, calls=%d", err, result, calls)
				}
			} else if !errors.Is(err, ErrRoundLimit) || calls != 2 || result.Rounds != 2 || len(result.ToolResults) != 2 {
				t.Fatalf("error=%v, result=%+v, calls=%d", err, result, calls)
			}
		})
	}
}
