package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
	agenttools "github.com/trinaldirizki/reverse-marketplace/internal/runtime/tools"
)

func TestFixedPriceToolPolicyBlocksMutationThroughLoop(t *testing.T) {
	service, lead, original, sends := fixedPriceFixture(t, false)
	tools, err := agentruntime.ApplyFixedPriceToolPolicy(original, true)
	if err != nil {
		t.Fatal(err)
	}
	definitions := make([]llm.ToolDefinition, len(original))
	for i, tool := range tools {
		definitions[i] = original[i].Definition()
		if !reflect.DeepEqual(tool.Definition(), definitions[i]) {
			t.Fatalf("tool definition changed: %s", definitions[i].Name)
		}
	}
	model := fixedPriceModel(func(_ context.Context, request llm.Request) (llm.Response, error) {
		if !reflect.DeepEqual(request.Tools, definitions) {
			t.Fatal("model did not receive the original six definitions")
		}
		return llm.Response{Continuation: &llm.Continuation{}, ToolCalls: []llm.ToolCall{
			{ID: "offer", Name: "make_offer", Args: json.RawMessage(`{"item_id":"item","price":5400000}`)},
			{ID: "checkout", Name: "create_checkout", Args: json.RawMessage(`{}`)},
			{ID: "escalate", Name: "escalate_to_seller", Args: json.RawMessage(`{"reason":"buyer asks"}`)},
			{ID: "close", Name: "close_lead", Args: json.RawMessage(`{"reason":"buyer asks"}`)},
			{ID: "read", Name: "get_item", Args: json.RawMessage(`{"item_id":"item"}`)},
			{ID: "send", Name: "send_message", Args: json.RawMessage(`{"text":"I can offer this item for Rp{price}."}`)},
		}}, nil
	})
	loop, err := agentruntime.NewToolLoop(model, tools, agentruntime.LoopLimits{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.Run(context.Background(), llm.Request{})
	if err != nil || !result.Stopped || len(result.ToolResults) != 6 {
		t.Fatalf("result=%+v, error=%v", result, err)
	}
	for _, output := range result.ToolResults[:4] {
		var rejection struct {
			Error contracts.Code `json:"error"`
		}
		if json.Unmarshal([]byte(output.Output), &rejection) != nil || rejection.Error != contracts.InvalidState {
			t.Fatalf("mutation was not rejected: %+v", output)
		}
	}
	if service.offers != 0 || service.checkouts != 0 || service.transitions != 0 || service.reads != 1 || *sends != 1 || lead.State != domain.LeadActive || lead.Checkout != nil {
		t.Fatalf("unexpected effects: service=%+v, sends=%d, lead=%+v", service, *sends, lead)
	}
	// Installing the fixed-price view must not replace the caller's legacy tools.
	for i, tool := range original {
		if tool == tools[i] {
			t.Fatalf("expected independent policy view for %s", tool.Definition().Name)
		}
	}
}

func TestFixedPriceToolPolicyDisabledPreservesLegacy(t *testing.T) {
	service, _, original, _ := fixedPriceFixture(t, false)
	tools, err := agentruntime.ApplyFixedPriceToolPolicy(original, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := range original {
		if tools[i] != original[i] {
			t.Fatal("disabled policy replaced a legacy tool")
		}
	}
	for _, call := range []struct{ name, args string }{
		{"make_offer", `{"item_id":"item","price":5400000}`},
		{"create_checkout", `{}`},
		{"escalate_to_seller", `{"reason":"buyer asks"}`},
		{"close_lead", `{"reason":"buyer asks"}`},
	} {
		for _, tool := range tools {
			if tool.Definition().Name == call.name {
				if _, err := tool.Run(context.Background(), json.RawMessage(call.args)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if service.offers != 1 || service.checkouts != 1 || service.transitions != 2 {
		t.Fatalf("legacy mutations did not reach service: %+v", service)
	}
}

func TestFixedPriceToolPolicyPreservesPendingAndCancellation(t *testing.T) {
	_, _, original, sends := fixedPriceFixture(t, true)
	tools, err := agentruntime.ApplyFixedPriceToolPolicy(original, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Definition().Name != "send_message" {
			continue
		}
		outcome, err := tool.Run(context.Background(), json.RawMessage(`{"text":"I can offer this item for Rp{price}."}`))
		if !errors.Is(err, agenttools.ErrSendMessagePending) || outcome.Stop || outcome.Output != `{"status":"pending"}` || *sends != 1 {
			t.Fatalf("pending changed: outcome=%+v, error=%v, sends=%d", outcome, err, *sends)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tool := range tools {
		if _, err := tool.Run(ctx, json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled %s returned %v", tool.Definition().Name, err)
		}
	}
}

func TestFixedPriceToolPolicyRejectsUnreviewedTools(t *testing.T) {
	for _, input := range [][]agentruntime.Tool{{nil}, {fixedPriceUnknownTool{}}} {
		if _, err := agentruntime.ApplyFixedPriceToolPolicy(input, true); err == nil {
			t.Fatal("fixed-price policy accepted an unreviewed tool")
		}
	}
}

type fixedPriceUnknownTool struct{}

func (fixedPriceUnknownTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "other_mutation"}
}
func (fixedPriceUnknownTool) Run(context.Context, json.RawMessage) (agentruntime.ToolOutcome, error) {
	panic("must not be called")
}

type fixedPriceModel func(context.Context, llm.Request) (llm.Response, error)

func (f fixedPriceModel) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	return f(ctx, request)
}

type fixedPriceCommerce struct {
	contracts.Commerce
	reads, offers, checkouts, transitions int
}

func (s *fixedPriceCommerce) GetItem(context.Context, string, string) (domain.Item, error) {
	s.reads++
	return domain.Item{ID: "item", SellerID: "seller", Active: true, AvailableStock: 2, ListPriceIDR: 5800000}, nil
}
func (s *fixedPriceCommerce) RecordOffer(context.Context, string, string, int64, int, string) (domain.Offer, error) {
	s.offers++
	return domain.Offer{}, &contracts.Error{Code: contracts.InvalidState}
}
func (s *fixedPriceCommerce) CreateCheckout(context.Context, string, string, string) (domain.Checkout, error) {
	s.checkouts++
	return domain.Checkout{}, &contracts.Error{Code: contracts.InvalidState}
}
func (s *fixedPriceCommerce) TransitionLead(context.Context, string, domain.LeadState, string) (domain.LeadRecord, error) {
	s.transitions++
	return domain.LeadRecord{}, &contracts.Error{Code: contracts.InvalidState}
}

func fixedPriceFixture(t *testing.T, pending bool) (*fixedPriceCommerce, *domain.LeadRecord, []agentruntime.Tool, *int) {
	t.Helper()
	service := &fixedPriceCommerce{}
	lead := &domain.LeadRecord{ID: "lead", SellerID: "seller", ItemID: "item", Platform: "mock", PostID: "post", ThreadID: "thread", BuyerID: "buyer", State: domain.LeadActive,
		Offer: &domain.Offer{ID: "offer", LeadID: "lead", ItemID: "item", PriceIDR: 5800000, Qty: 1}}
	key := func(context.Context, string) (string, error) { return "action", nil }
	tools, err := agenttools.NewCommerceTools(service, lead, key)
	if err != nil {
		t.Fatal(err)
	}
	sends := 0
	send, err := agenttools.NewSendMessageTool(func(_ context.Context, request contracts.AgentMessageRequest) (contracts.AgentDeliveryResult, error) {
		sends++
		if pending {
			return contracts.AgentDeliveryResult{Status: contracts.AgentDeliveryPending}, nil
		}
		return contracts.AgentDeliveryResult{Status: contracts.AgentDeliveryDelivered, Receipt: contracts.AgentDeliveryReceipt{ActionID: request.ActionID, MessageID: "message", PublishedAt: time.Date(2026, time.November, 7, 6, 0, 0, 0, time.UTC)}}, nil
	}, lead, key, agenttools.SendMessageOptions{CheckoutOrigin: "http://localhost:8080"})
	if err != nil {
		t.Fatal(err)
	}
	return service, lead, append(tools, send), &sends
}
