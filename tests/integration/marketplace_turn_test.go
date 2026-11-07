package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/commercefake"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/marketplacetest"
	"github.com/trinaldirizki/reverse-marketplace/internal/runtime"
)

// This exercises the existing worker and tool loop using test-only intake and
// tool wrappers, and the P2 commerce fake. All retained state is in memory.
func TestMarketplaceTurnsWithFixtureAdapters(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	commerce := commercefake.NewSeeded()
	lead, err := commerce.GetLead(ctx, commercefake.LeadID)
	if err != nil {
		t.Fatal(err)
	}
	lead.Offer = &domain.Offer{ID: "validated-offer", LeadID: lead.ID, ItemID: lead.ItemID, PriceIDR: 5_400_000, Qty: 1}
	commerce.GetLeadFunc = func(context.Context, string) (domain.LeadRecord, error) { return lead, nil }
	post := domain.Post{PostRef: domain.PostRef{Platform: lead.Platform, ExternalPostID: lead.PostID},
		BuyerID: lead.BuyerID, ThreadID: lead.ThreadID, Text: "Looking for a used laptop", CreatedAt: now}
	fixture, err := marketplacetest.NewConversation(ctx, post, clock)
	if err != nil {
		t.Fatal(err)
	}
	thread := domain.ThreadRef{Platform: lead.Platform, ThreadID: lead.ThreadID}
	const paymentLink = "http://localhost:8080/checkout/fixture-token"
	sender, err := marketplace.NewAgentSender(commerce, fixture.Threads, fixture.Threads, "http://localhost:8080", clock)
	if err != nil {
		t.Fatal(err)
	}

	commerce.CreateCheckoutFunc = func(ctx context.Context, leadID, offerID, _ string) (domain.Checkout, error) {
		history, err := fixture.Threads.ThreadHistory(ctx, thread)
		if err != nil || len(history) != 2 {
			return domain.Checkout{}, fmt.Errorf("expected initial offer and buyer reply before checkout, got %d messages: %w", len(history), err)
		}
		lead.Checkout = &domain.Checkout{ID: "fixture-checkout", LeadID: leadID, OfferID: offerID, ItemID: lead.ItemID,
			PriceIDR: lead.Offer.PriceIDR, Qty: 1, TotalIDR: lead.Offer.PriceIDR, URL: paymentLink,
			Status: domain.CheckoutPending, ExpiresAt: now.Add(30 * time.Minute)}
		lead.State = domain.LeadReserved
		return *lead.Checkout, nil
	}

	type completedTurn struct {
		event   runtime.Event
		receipt contracts.AgentDeliveryReceipt
		err     error
	}
	completed := make(chan completedTurn, 8)
	worker, err := runtime.NewWorker(8, func(ctx context.Context, event runtime.Event) error {
		var receipt contracts.AgentDeliveryReceipt
		checkoutID := ""
		tools := []runtime.Tool{
			fixtureTool{"create_checkout", func(ctx context.Context, _ json.RawMessage) (runtime.ToolOutcome, error) {
				checkout, err := commerce.CreateCheckout(ctx, event.LeadID, lead.Offer.ID, event.ID+"/checkout")
				if err != nil {
					return runtime.ToolOutcome{}, err
				}
				checkoutID = checkout.ID
				history, err := fixture.Threads.ThreadHistory(ctx, thread)
				if err != nil || len(history) != 2 {
					return runtime.ToolOutcome{}, errors.New("checkout creation changed buyer-visible history")
				}
				return runtime.ToolOutcome{Output: `{"ok":true}`}, nil
			}},
			fixtureTool{"send_message", func(ctx context.Context, args json.RawMessage) (runtime.ToolOutcome, error) {
				var input struct{ Text string }
				if err := json.Unmarshal(args, &input); err != nil {
					return runtime.ToolOutcome{}, err
				}
				result, err := sender.SendAgentMessage(ctx, contracts.AgentMessageRequest{ActionID: event.ID + "/send",
					LeadID: event.LeadID, SellerID: lead.SellerID, Thread: thread, CheckoutID: checkoutID,
					Channel: contracts.AgentPublicReply, Text: input.Text})
				receipt = result.Receipt
				return runtime.ToolOutcome{Output: `{"ok":true}`, Stop: result.Status == contracts.AgentDeliveryDelivered}, err
			}},
			fixtureTool{"unexpected", func(context.Context, json.RawMessage) (runtime.ToolOutcome, error) {
				return runtime.ToolOutcome{}, errors.New("send_message did not end the turn")
			}},
		}
		text := marketplace.AgentOfferText
		var calls []llm.ToolCall
		if event.Trigger == runtime.TriggerBuyerReply {
			text = marketplace.AgentCheckoutText
			calls = append(calls, llm.ToolCall{ID: "checkout", Name: "create_checkout", Args: json.RawMessage(`{}`)})
		}
		args, _ := json.Marshal(map[string]string{"text": text})
		calls = append(calls, llm.ToolCall{ID: "send", Name: "send_message", Args: args}, llm.ToolCall{ID: "later", Name: "unexpected", Args: json.RawMessage(`{}`)})
		loop, err := runtime.NewToolLoop(fixtureModel{calls}, tools, runtime.LoopLimits{})
		if err == nil {
			result, runErr := loop.Run(ctx, llm.Request{})
			err = runErr
			if err == nil && !result.Stopped {
				err = errors.New("expected delivery to stop the turn")
			}
		}
		completed <- completedTurn{event: event, receipt: receipt, err: err}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	waitTurn := func(eventID string) contracts.AgentDeliveryReceipt {
		t.Helper()
		select {
		case turn := <-completed:
			if turn.event.ID != eventID || turn.err != nil || turn.receipt.MessageID == "" {
				t.Fatalf("turn = %#v", turn)
			}
			return turn.receipt
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return contracts.AgentDeliveryReceipt{}
		}
	}
	leadID := lead.ID
	for range 2 {
		if err := worker.OnLeadDiscovered(ctx, "discovery", leadID); err != nil {
			t.Fatal(err)
		}
	}
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx) }()
	defer func() {
		cancel()
		<-workerDone
	}()
	first := waitTurn("discovery")
	input := contracts.BuyerReplyInput{ExternalReplyID: "buyer-reply", Text: "I'll take it", ReplyToMessageID: first.MessageID}
	reply, err := fixture.Threads.SubmitBuyerReply(ctx, thread, input)
	if err != nil {
		t.Fatal(err)
	}
	intake := fixtureIntake(func(ctx context.Context, event contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error) {
		if event.Message.LeadID != leadID {
			return "", errors.New("buyer reply did not route to the fixture lead")
		}
		err := worker.Enqueue(ctx, runtime.Event{ID: event.ID, LeadID: leadID, Trigger: runtime.TriggerBuyerReply, MessageID: event.Message.ID})
		return contracts.BuyerReplyAcceptedVolatile, err
	})
	for range 2 {
		if _, err := fixture.Threads.SubmitBuyerReply(ctx, thread, input); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Threads.RelayBuyerReplies(ctx, intake); err != nil {
			t.Fatal(err)
		}
	}
	second := waitTurn(reply.Event.ID)
	history, err := fixture.Threads.ThreadHistory(ctx, thread)
	if err != nil || len(history) != 3 {
		t.Fatalf("expected offer, reply, and checkout message: %d messages, %v", len(history), err)
	}
	for _, message := range history {
		if message.ID == second.MessageID && message.Text == "Checkout for Rp 5400000: "+paymentLink {
			return
		}
	}
	t.Fatal("checkout receipt did not match the buyer-visible checkout message")
}

type fixtureModel struct{ calls []llm.ToolCall }

func (f fixtureModel) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{ToolCalls: f.calls, Continuation: &llm.Continuation{}}, nil
}

type fixtureTool struct {
	name string
	run  func(context.Context, json.RawMessage) (runtime.ToolOutcome, error)
}

func (f fixtureTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: f.name, Schema: json.RawMessage(`{"type":"object"}`)}
}

func (f fixtureTool) Run(ctx context.Context, args json.RawMessage) (runtime.ToolOutcome, error) {
	return f.run(ctx, args)
}

type fixtureIntake func(context.Context, contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error)

func (f fixtureIntake) AcceptBuyerReply(ctx context.Context, event contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error) {
	return f(ctx, event)
}
