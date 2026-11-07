package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/commercefake"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"github.com/trinaldirizki/reverse-marketplace/internal/runtime/actionlog"
)

func TestDemoTraceShowsActualModelRoundsAndSafeToolOutcomes(t *testing.T) {
	var output bytes.Buffer
	sink, _ := actionlog.NewWriterSink(&output)
	recorder, _ := actionlog.NewRecorder(sink)
	rounds := 0
	model := loopModelFunc(func(context.Context, llm.Request) (llm.Response, error) {
		rounds++
		name := "get_item"
		if rounds == 2 {
			name = "send_message"
		}
		return llm.Response{Text: "PRIVATE model prose", Continuation: &llm.Continuation{}, ToolCalls: []llm.ToolCall{{ID: "PRIVATE call ID", Name: name, Args: json.RawMessage(`{"text":"PRIVATE model arguments"}`)}}}, nil
	})
	factory := func(context.Context, *LeadContext, TurnMetadata) ([]Tool, error) {
		return []Tool{
			loopTestTool{name: "get_item", run: func(context.Context, json.RawMessage) (ToolOutcome, error) {
				return ToolOutcome{Output: `{"private":"PRIVATE inventory facts"}`}, nil
			}},
			loopTestTool{name: "send_message", run: func(context.Context, json.RawMessage) (ToolOutcome, error) {
				return ToolOutcome{Stop: true, Output: `{"status":"pending","private":"PRIVATE checkout token"}`}, nil
			}},
		}, nil
	}
	runner, err := NewTurnRunner(commercefake.NewSeeded(), model, factory, LoopLimits{}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	input := auditTurnInput()
	input.Event.ID = "PRIVATE event\nforged-log"
	result, err := runner.RunTurn(t.Context(), input)
	if err != nil || !result.Stopped || result.Rounds != 2 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	trace := output.String()
	t.Logf("safe demo trace:\n%s", trace)
	if strings.Count(trace, "model_round |") != 2 || !strings.Contains(trace, "get_item") || !strings.Contains(trace, "send_message") || !strings.Contains(trace, "DELIVERY_PENDING") {
		t.Fatalf("missing actual loop behavior: %s", trace)
	}
	if strings.Contains(trace, "PRIVATE") || strings.Contains(trace, "private prompt") || strings.Contains(trace, "forged-log") || strings.Contains(trace, "message delivery succeeded") {
		t.Fatalf("trace leaked content or false delivery: %s", trace)
	}
	if strings.Index(trace, "turn_started") > strings.Index(trace, "model_round") || strings.Index(trace, "send_message") > strings.Index(trace, "turn_completed") {
		t.Fatal("trace lifecycle out of order")
	}
}

func TestDemoTraceRoundWriteFailurePreventsModelExecution(t *testing.T) {
	modelCalls := 0
	recorder, _ := actionlog.NewRecorder(turnAuditSink(func(_ context.Context, e actionlog.Entry) error {
		if e.Kind == actionlog.KindModelRound {
			return errors.New("PRIVATE trace write failure")
		}
		return nil
	}))
	model := loopModelFunc(func(context.Context, llm.Request) (llm.Response, error) { modelCalls++; return llm.Response{}, nil })
	factory := func(context.Context, *LeadContext, TurnMetadata) ([]Tool, error) { return nil, nil }
	runner, _ := NewTurnRunner(commercefake.NewSeeded(), model, factory, LoopLimits{}, recorder)
	_, err := runner.RunTurn(t.Context(), auditTurnInput())
	if !errors.Is(err, ErrTurnAudit) || modelCalls != 0 || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("calls=%d error=%v", modelCalls, err)
	}
}
