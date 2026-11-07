package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
)

type scopeJSONGenerator func(context.Context, llm.JSONRequest, any) error

func (f scopeJSONGenerator) GenerateJSON(ctx context.Context, request llm.JSONRequest, output any) error {
	return f(ctx, request, output)
}

// This fixture is not an approved P2 schema or a semantic classifier.
func scopeJSONFixture() (llm.JSONRequest, func(json.RawMessage) (ScopeDecision, error)) {
	return llm.JSONRequest{Instructions: "Test classification instructions", SchemaName: "test_scope", Schema: json.RawMessage(`{"type":"object"}`)}, func(raw json.RawMessage) (ScopeDecision, error) {
		var result struct {
			Scope ScopeDecision `json:"scope"`
		}
		err := json.Unmarshal(raw, &result)
		return result.Scope, err
	}
}

func TestJSONScopeClassifierGuardsNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		failure      error
		want         error
		negotiation  int
	}{
		{"allowed", `{"scope":"in_scope"}`, nil, nil, 1},
		{"off topic", `{"scope":"off_topic"}`, nil, ErrScopeBlocked, 0},
		{"mixed", `{"scope":"mixed"}`, nil, ErrScopeMixedUnsupported, 0},
		{"unknown", `{"scope":"new_decision"}`, nil, ErrScopeUnavailable, 0},
		{"missing decision", `{}`, nil, ErrScopeUnavailable, 0},
		{"bad output", `not JSON`, nil, ErrScopeUnavailable, 0},
		{"refusal", "", llm.ErrRefusal, ErrScopeUnavailable, 0},
		{"provider failure", "", errors.New("private buyer text"), ErrScopeUnavailable, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			template, decode := scopeJSONFixture()
			input := scopeInput("Buyer request")
			input.Instructions = "PRIVATE seller policy"
			input.TriggerInstruction = "PRIVATE trigger context"
			calls, turns := 0, 0
			generator := scopeJSONGenerator(func(_ context.Context, request llm.JSONRequest, output any) error {
				calls++
				if strings.Contains(request.Input, "PRIVATE") {
					t.Fatal("classifier received trusted negotiation instructions")
				}
				var snapshot struct {
					History []llm.ChatMessage `json:"history"`
				}
				if err := json.Unmarshal([]byte(request.Input), &snapshot); err != nil || len(snapshot.History) != 1 || snapshot.History[0] != input.History[0] {
					t.Fatalf("classifier did not receive buyer snapshot: %v", err)
				}
				if tc.failure != nil {
					return tc.failure
				}
				*output.(*json.RawMessage) = json.RawMessage(tc.output)
				return nil
			})
			classify, err := NewJSONScopeClassifier(generator, template, decode)
			if err != nil {
				t.Fatal(err)
			}
			guard, err := NewScopeGuard(classify, func(context.Context, TurnInput) (LoopResult, error) { turns++; return LoopResult{Stopped: true}, nil }, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = guard.RunTurn(t.Context(), input)
			if !errors.Is(err, tc.want) || turns != tc.negotiation || calls != 1 {
				t.Fatalf("calls=%d turns=%d error=%v", calls, turns, err)
			}
			if err != nil && strings.Contains(err.Error(), "private buyer text") {
				t.Fatal("provider error leaked buyer data")
			}
		})
	}
}

func TestJSONScopeClassifierRejectsAbsentAssetsAndCancellation(t *testing.T) {
	template, decode := scopeJSONFixture()
	called := false
	generator := scopeJSONGenerator(func(context.Context, llm.JSONRequest, any) error { called = true; return nil })
	if _, err := NewJSONScopeClassifier(nil, template, decode); err == nil {
		t.Fatal("accepted absent generator")
	}
	if _, err := NewJSONScopeClassifier(generator, llm.JSONRequest{}, decode); err == nil {
		t.Fatal("accepted absent owner assets")
	}
	if _, err := NewJSONScopeClassifier(generator, template, nil); err == nil {
		t.Fatal("accepted absent decoder")
	}
	classify, err := NewJSONScopeClassifier(generator, template, decode)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = classify(ctx, scopeInput("How much?"))
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("called=%v error=%v", called, err)
	}
}
