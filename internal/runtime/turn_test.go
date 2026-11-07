package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/commercefake"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
)

func TestTurnRunnerComposesOpeningAndReplyWithFreshTools(t *testing.T) {
	for _, trigger := range []Trigger{TriggerLeadDiscovered, TriggerBuyerReply} {
		t.Run(string(trigger), func(t *testing.T) {
			event := Event{ID: "event-" + string(trigger), LeadID: commercefake.LeadID, Trigger: trigger}
			var history []llm.ChatMessage
			if trigger == TriggerBuyerReply {
				event.MessageID = "reply-1"
				history = []llm.ChatMessage{{Role: "assistant", Content: "Opening offer"}, {Role: "user", Content: "Bisa kurang?"}}
			}
			rounds, sends := 0, 0
			var actionKeys []string
			var message string
			model := loopModelFunc(func(_ context.Context, req llm.Request) (llm.Response, error) {
				rounds++
				if rounds == 1 {
					if len(req.Messages) != len(history)+3 {
						t.Fatal("turn context or trigger missing")
					}
					for i, want := range history {
						if req.Messages[i+2] != want {
							t.Fatal("caller history reordered")
						}
					}
					return llm.Response{Continuation: &llm.Continuation{}, ToolCalls: []llm.ToolCall{{ID: "offer-a", Name: "make_offer", Args: json.RawMessage(`{}`)}}}, nil
				}
				if rounds == 2 {
					if len(req.ToolResults) != 1 || req.ToolResults[0].CallID != "offer-a" {
						t.Fatal("first tool result not continued")
					}
					return llm.Response{Continuation: &llm.Continuation{}, ToolCalls: []llm.ToolCall{{ID: "offer-b", Name: "make_offer", Args: json.RawMessage(`{}`)}}}, nil
				}
				return llm.Response{Continuation: &llm.Continuation{}, ToolCalls: []llm.ToolCall{
					{ID: "send", Name: "send_message", Args: json.RawMessage(`{}`)},
					{ID: "must-not-run", Name: "make_offer", Args: json.RawMessage(`{}`)},
				}}, nil
			})
			factory := func(_ context.Context, loaded *LeadContext, meta TurnMetadata) ([]Tool, error) {
				if meta.Event != event || loaded.Lead.ID != event.LeadID {
					t.Fatal("trusted event identity lost")
				}
				return []Tool{
					loopTestTool{name: "make_offer", run: func(ctx context.Context, _ json.RawMessage) (ToolOutcome, error) {
						key, err := meta.ActionKey(ctx, "make_offer")
						if err != nil {
							return ToolOutcome{}, err
						}
						same, err := meta.ActionKey(ctx, "make_offer")
						if err != nil || key != same {
							t.Fatal("operation identity changed within the same call")
						}
						if _, err := meta.ActionKey(ctx, "send_message"); err == nil {
							t.Fatal("another tool borrowed the action identity")
						}
						actionKeys = append(actionKeys, key)
						loaded.Lead.Offer = &domain.Offer{ID: key, LeadID: loaded.Lead.ID, ItemID: loaded.Item.ID, PriceIDR: 5_600_000 - int64(len(actionKeys)-1)*100_000, Qty: 1}
						return ToolOutcome{Output: "offer recorded"}, nil
					}},
					loopTestTool{name: "send_message", run: func(context.Context, json.RawMessage) (ToolOutcome, error) {
						sends++
						message = fmt.Sprintf("Offer Rp%d", loaded.Lead.Offer.PriceIDR)
						return ToolOutcome{Output: "captured by test", Stop: true}, nil
					}},
				}, nil
			}
			runner, err := NewTurnRunner(commercefake.NewSeeded(), model, factory, LoopLimits{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.RunTurn(context.Background(), TurnInput{Event: event, Instructions: "Use the registered tools.", TriggerInstruction: "Respond to this event.", History: history})
			if err != nil {
				t.Fatal(err)
			}
			if sends != 1 || message != "Offer Rp5500000" || !result.Stopped || result.ToolCalls != 3 || rounds != 3 {
				t.Fatalf("incorrect outcome: message=%q sends=%d result=%+v", message, sends, result)
			}
			if len(actionKeys) != 2 || actionKeys[0] == actionKeys[1] {
				t.Fatal("same-name operations reused an identity")
			}
			var identity []string
			if json.Unmarshal([]byte(actionKeys[0]), &identity) != nil || len(identity) != 2 || identity[0] != event.ID || identity[1] != "offer-a" {
				t.Fatal("action identity not derived from event and actual call")
			}
		})
	}
}

func TestTurnRunnerStopsInactiveLeadBeforeModelOrFactory(t *testing.T) {
	fake := commercefake.NewSeeded()
	getLead := fake.GetLeadFunc
	fake.GetLeadFunc = func(ctx context.Context, id string) (domain.LeadRecord, error) {
		lead, err := getLead(ctx, id)
		lead.State = domain.LeadReserved
		return lead, err
	}
	model := loopModelFunc(func(context.Context, llm.Request) (llm.Response, error) {
		t.Fatal("inactive lead reached model")
		return llm.Response{}, nil
	})
	factory := func(context.Context, *LeadContext, TurnMetadata) ([]Tool, error) {
		t.Fatal("inactive lead reached tools")
		return nil, nil
	}
	runner, err := NewTurnRunner(fake, model, factory, LoopLimits{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunTurn(context.Background(), TurnInput{Event: Event{ID: "event", LeadID: commercefake.LeadID, Trigger: TriggerBuyerReply}, Instructions: "Use tools."})
	if !errors.Is(err, ErrLeadNotActive) {
		t.Fatalf("error=%v", err)
	}
}

func TestTurnRunnerPropagatesFailure(t *testing.T) {
	for _, stage := range []string{"load", "tool"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("dependency failed")
			fake := commercefake.NewSeeded()
			if stage == "load" {
				fake.GetLeadFunc = func(context.Context, string) (domain.LeadRecord, error) { return domain.LeadRecord{}, failure }
			}
			model := loopModelFunc(func(context.Context, llm.Request) (llm.Response, error) {
				if stage == "load" {
					t.Fatal("failed load reached model")
				}
				return llm.Response{Continuation: &llm.Continuation{}, ToolCalls: []llm.ToolCall{{ID: "call", Name: "get_item", Args: json.RawMessage(`{}`)}}}, nil
			})
			factory := func(context.Context, *LeadContext, TurnMetadata) ([]Tool, error) {
				return []Tool{loopTestTool{name: "get_item", run: func(context.Context, json.RawMessage) (ToolOutcome, error) { return ToolOutcome{}, failure }}}, nil
			}
			runner, err := NewTurnRunner(fake, model, factory, LoopLimits{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.RunTurn(context.Background(), TurnInput{Event: Event{ID: "event", LeadID: commercefake.LeadID, Trigger: TriggerLeadDiscovered}, Instructions: "Use tools."})
			if !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
