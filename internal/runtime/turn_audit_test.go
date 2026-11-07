package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/commercefake"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"github.com/trinaldirizki/reverse-marketplace/internal/runtime/actionlog"
)

type turnAuditSink func(context.Context, actionlog.Entry) error

func (f turnAuditSink) Append(ctx context.Context, e actionlog.Entry) error { return f(ctx, e) }

func auditTurnInput() TurnInput {
	return TurnInput{Event: Event{ID: "mock:reply:123", LeadID: commercefake.LeadID, Trigger: TriggerBuyerReply}, Instructions: "Use tools; private prompt marker", TriggerInstruction: "Reply"}
}

func TestTurnAuditRecordsSafeLifecycleAndPendingStatus(t *testing.T) {
	sink, _ := actionlog.NewMemorySink(12)
	recorder, _ := actionlog.NewRecorder(sink)
	model := loopModelFunc(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Continuation: &llm.Continuation{}, ToolCalls: []llm.ToolCall{{ID: "call:send:1", Name: "send_message", Args: json.RawMessage(`{"text":"private model args"}`)}}}, nil
	})
	factory := func(context.Context, *LeadContext, TurnMetadata) ([]Tool, error) {
		return []Tool{loopTestTool{name: "send_message", run: func(context.Context, json.RawMessage) (ToolOutcome, error) {
			return ToolOutcome{Stop: true, Output: `{"status":"pending","private":"https://checkout.invalid/private-token"}`}, nil
		}}}, nil
	}
	runner, err := NewTurnRunner(commercefake.NewSeeded(), model, factory, LoopLimits{}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(context.Background(), auditTurnInput())
	if err != nil || !result.Stopped {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	var lifecycle []actionlog.Kind
	pending := false
	for _, e := range sink.Entries() {
		if e.EventID != "mock:reply:123" || e.SellerID != commercefake.SellerID {
			t.Fatalf("identity lost: %+v", e)
		}
		lifecycle = append(lifecycle, e.Kind)
		if e.Kind == actionlog.KindTool {
			pending = e.Code == actionlog.CodePending && e.ToolCallID == "call:send:1"
			if strings.Contains(e.Summary(), "succeeded") {
				t.Fatal("pending send described as delivered")
			}
		}
		data, _ := json.Marshal(e)
		for _, secret := range []string{"private prompt", "private model", "private-token", "checkout.invalid", "5400000"} {
			if strings.Contains(string(data), secret) || strings.Contains(e.Summary(), secret) {
				t.Fatal("private content entered audit")
			}
		}
	}
	if !pending || len(lifecycle) != 5 || lifecycle[0] != actionlog.KindContextLoaded || lifecycle[1] != actionlog.KindTurnStarted || lifecycle[2] != actionlog.KindModelRound || lifecycle[4] != actionlog.KindTurnCompleted {
		t.Fatalf("missing lifecycle or pending record: %v", lifecycle)
	}
}

func TestTurnAuditFailureBeforeActionStopsExecution(t *testing.T) {
	calls := 0
	cause := errors.New("private sink failure")
	recorder, _ := actionlog.NewRecorder(turnAuditSink(func(context.Context, actionlog.Entry) error { return cause }))
	model := loopModelFunc(func(context.Context, llm.Request) (llm.Response, error) { calls++; return llm.Response{}, nil })
	factory := func(context.Context, *LeadContext, TurnMetadata) ([]Tool, error) { calls++; return nil, nil }
	runner, _ := NewTurnRunner(commercefake.NewSeeded(), model, factory, LoopLimits{}, recorder)
	_, err := runner.RunTurn(context.Background(), auditTurnInput())
	if !errors.Is(err, ErrTurnAudit) || !errors.Is(err, cause) || calls != 0 || strings.Contains(err.Error(), "private") {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestTurnAuditFailureAfterActionRetainsPartialResult(t *testing.T) {
	effects := 0
	recorder, _ := actionlog.NewRecorder(turnAuditSink(func(_ context.Context, e actionlog.Entry) error {
		if e.Kind == actionlog.KindTool {
			return errors.New("private log failure")
		}
		return nil
	}))
	model := loopModelFunc(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Continuation: &llm.Continuation{}, ToolCalls: []llm.ToolCall{{ID: "offer-a", Name: "make_offer", Args: json.RawMessage(`{}`)}, {ID: "offer-b", Name: "make_offer", Args: json.RawMessage(`{}`)}}}, nil
	})
	factory := func(context.Context, *LeadContext, TurnMetadata) ([]Tool, error) {
		return []Tool{loopTestTool{name: "make_offer", run: func(context.Context, json.RawMessage) (ToolOutcome, error) {
			effects++
			return ToolOutcome{Output: `{"status":"offer_recorded"}`}, nil
		}}}, nil
	}
	runner, _ := NewTurnRunner(commercefake.NewSeeded(), model, factory, LoopLimits{}, recorder)
	result, err := runner.RunTurn(context.Background(), auditTurnInput())
	if !errors.Is(err, ErrTurnAudit) || effects != 1 || len(result.ToolResults) != 1 || result.ToolResults[0].CallID != "offer-a" {
		t.Fatalf("error=%v effects=%d result=%+v", err, effects, result)
	}
}

func TestTurnAuditRetainsPendingOutcomeWithError(t *testing.T) {
	sink, _ := actionlog.NewMemorySink(12)
	recorder, _ := actionlog.NewRecorder(sink)
	failure := errors.New("publication unconfirmed")
	model := loopModelFunc(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Continuation: &llm.Continuation{}, ToolCalls: []llm.ToolCall{{ID: "send-a", Name: "send_message", Args: json.RawMessage(`{}`)}}}, nil
	})
	factory := func(context.Context, *LeadContext, TurnMetadata) ([]Tool, error) {
		return []Tool{loopTestTool{name: "send_message", run: func(context.Context, json.RawMessage) (ToolOutcome, error) {
			return ToolOutcome{Output: `{"status":"pending"}`}, failure
		}}}, nil
	}
	runner, _ := NewTurnRunner(commercefake.NewSeeded(), model, factory, LoopLimits{}, recorder)
	result, err := runner.RunTurn(context.Background(), auditTurnInput())
	if !errors.Is(err, failure) || len(result.ToolResults) != 1 {
		t.Fatalf("pending result lost: %+v %v", result, err)
	}
	found := false
	for _, e := range sink.Entries() {
		if e.Tool == actionlog.ToolSendMessage {
			found = true
			if e.Code != actionlog.CodePending {
				t.Fatalf("unconfirmed delivery: %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("pending delivery absent from audit")
	}
}

func TestTurnAuditProjectsBusinessRejectionWithoutRawOutput(t *testing.T) {
	sink, _ := actionlog.NewMemorySink(12)
	recorder, _ := actionlog.NewRecorder(sink)
	rounds := 0
	model := loopModelFunc(func(context.Context, llm.Request) (llm.Response, error) {
		rounds++
		if rounds > 1 {
			return llm.Response{Text: "stop"}, nil
		}
		return llm.Response{Continuation: &llm.Continuation{}, ToolCalls: []llm.ToolCall{{ID: "offer:blocked", Name: "make_offer", Args: json.RawMessage(`{}`)}}}, nil
	})
	factory := func(context.Context, *LeadContext, TurnMetadata) ([]Tool, error) {
		return []Tool{loopTestTool{name: "make_offer", run: func(context.Context, json.RawMessage) (ToolOutcome, error) {
			return ToolOutcome{Output: `{"error":"PRICE_BELOW_MINIMUM","reason":"private minimum 5400000"}`}, nil
		}}}, nil
	}
	runner, _ := NewTurnRunner(commercefake.NewSeeded(), model, factory, LoopLimits{}, recorder)
	if _, err := runner.RunTurn(context.Background(), auditTurnInput()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range sink.Entries() {
		if e.ToolCallID == "offer:blocked" {
			found = true
			if e.Kind != actionlog.KindGuardrail || e.Status != actionlog.StatusBlocked || e.Code != actionlog.CodePriceBelowMinimum {
				t.Fatalf("incorrect rejection: %+v", e)
			}
		}
		data, _ := json.Marshal(e)
		if strings.Contains(string(data), "5400000") || strings.Contains(e.Summary(), "5400000") {
			t.Fatal("private rejection detail recorded")
		}
	}
	if !found {
		t.Fatal("business rejection absent from audit")
	}
}
