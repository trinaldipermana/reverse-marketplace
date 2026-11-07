package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/marketplacetest"
	"github.com/trinaldirizki/reverse-marketplace/internal/runtime"
	agenttools "github.com/trinaldirizki/reverse-marketplace/internal/runtime/tools"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

// These local service tests use the approved English P4 templates and a scripted
// model. They do not exercise language understanding, durable event intake, or UI.
func TestPurchaseThroughRealConversationTools(t *testing.T) {
	f := newPurchaseFixture(t)
	model := &purchaseModel{t: t, steps: []func(llm.Request) llm.ToolCall{
		func(req llm.Request) llm.ToolCall {
			var names []string
			for _, tool := range req.Tools {
				names = append(names, tool.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, []string{"close_lead", "create_checkout", "escalate_to_seller", "get_item", "make_offer", "send_message"}) {
				t.Fatalf("conversation tool set = %v", names)
			}
			return purchaseCall("item", "get_item", `{"item_id":"item_thinkpad"}`)
		},
		func(llm.Request) llm.ToolCall {
			return purchaseCall("offer", "make_offer", `{"item_id":"item_thinkpad","price":5400000}`)
		},
		func(llm.Request) llm.ToolCall {
			lead := f.loadLead(t)
			if lead.Offer == nil || lead.Offer.PriceIDR != 5_400_000 || lead.State != domain.LeadActive || len(f.history(t)) != 0 {
				t.Fatal("make_offer must store terms without publishing or reserving")
			}
			return purchaseTextCall(t, "offer-message", marketplace.AgentOfferText)
		},
		func(req llm.Request) llm.ToolCall {
			found := false
			for _, message := range req.Messages {
				found = found || message.Role == "user" && message.Content == "I'll take it"
			}
			if !found {
				t.Fatal("acceptance turn did not receive the saved buyer reply")
			}
			return purchaseCall("checkout", "create_checkout", `{}`)
		},
		func(llm.Request) llm.ToolCall {
			lead := f.loadLead(t)
			if lead.State != domain.LeadReserved || lead.Checkout == nil || lead.Checkout.OfferID != lead.Offer.ID || len(f.history(t)) != 2 {
				t.Fatal("checkout must reserve the stored offer without publishing a message")
			}
			f.assertStock(t, 2, 1, 1)
			return purchaseTextCall(t, "checkout-message", marketplace.AgentCheckoutText)
		},
	}}
	runner := f.runner(t, model)
	var eventID string
	if err := f.db.QueryRowContext(t.Context(), `SELECT event_id FROM runtime_notifications WHERE lead_id=?`, f.lead.ID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	f.run(t, runner, runtime.Event{ID: eventID, LeadID: f.lead.ID, Trigger: runtime.TriggerLeadDiscovered}, nil)
	history := f.history(t)
	if len(history) != 1 || history[0].Direction != domain.MessageAgent || history[0].LeadID != f.lead.ID || history[0].Text != "I can offer this item for Rp 5400000." {
		t.Fatalf("expected one grounded offer message, got %+v", history)
	}
	f.assertStock(t, 2, 0, 2)
	f.advance()
	reply, err := f.conversation.Threads.SubmitBuyerReply(t.Context(), f.thread, contracts.BuyerReplyInput{
		ExternalReplyID: "purchase-accept", Text: "I'll take it", ReplyToMessageID: history[0].ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Event.Message.LeadID != f.lead.ID || reply.Event.Message.SellerID != f.lead.SellerID {
		t.Fatal("buyer reply did not route to the offered lead")
	}
	var messages []llm.ChatMessage
	for _, message := range f.history(t) {
		role := "assistant"
		if message.Direction == domain.MessageBuyer {
			role = "user"
		}
		messages = append(messages, llm.ChatMessage{Role: role, Content: message.Text})
	}
	f.advance()
	f.run(t, runner, runtime.Event{ID: reply.Event.ID, LeadID: f.lead.ID, Trigger: runtime.TriggerBuyerReply, MessageID: reply.Event.Message.ID}, messages)
	model.assertConsumed()
	reserved := f.loadLead(t)
	history = f.history(t)
	if len(history) != 3 || history[1].Direction != domain.MessageBuyer || history[2].Direction != domain.MessageAgent || reserved.Checkout == nil {
		t.Fatal("expected ordered offer, acceptance and checkout publication")
	}
	const prefix = "Checkout for Rp 5400000: "
	if history[2].Text != prefix+reserved.Checkout.URL || reserved.Checkout.PriceIDR != reserved.Offer.PriceIDR || reserved.Checkout.Qty != 1 {
		t.Fatal("published checkout did not preserve the stored offer terms")
	}
	// Pay using the link actually published to the buyer, not a fabricated token.
	link, err := url.Parse(strings.TrimPrefix(history[2].Text, prefix))
	if err != nil || link.Scheme+"://"+link.Host != purchaseOrigin || !strings.HasPrefix(link.Path, "/checkout/") {
		t.Fatal("checkout publication did not contain the configured payment destination")
	}
	token := strings.TrimPrefix(link.Path, "/checkout/")
	payment, err := f.commerce.PayCheckout(t.Context(), token, "buyer-pay")
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.commerce.PayCheckout(t.Context(), token, "buyer-repeat-pay")
	if err != nil || payment.ID == "" || payment != again || payment.LeadID != f.lead.ID || payment.AmountIDR != 5_400_000 || payment.Quantity != 1 {
		t.Fatalf("payment/repeated payment mismatch: %v", err)
	}
	paid := f.loadLead(t)
	if paid.State != domain.LeadPaid || paid.Checkout.Status != domain.CheckoutPaid {
		t.Fatal("mock payment did not commit the paid lead and checkout")
	}
	f.assertStock(t, 1, 0, 1)
	for _, query := range []string{`SELECT count(*) FROM payments`, `SELECT count(*) FROM stock_movements WHERE kind='payment'`} {
		var count int
		if err := f.db.QueryRowContext(t.Context(), query).Scan(&count); err != nil || count != 1 {
			t.Fatalf("expected one payment/stock commit, got %d: %v", count, err)
		}
	}
}

func TestPurchaseRejectsBelowFloorBeforePublication(t *testing.T) {
	f := newPurchaseFixture(t)
	model := &purchaseModel{t: t, steps: []func(llm.Request) llm.ToolCall{
		func(llm.Request) llm.ToolCall {
			return purchaseCall("illegal-offer", "make_offer", `{"item_id":"item_thinkpad","price":5100000}`)
		},
		func(req llm.Request) llm.ToolCall {
			assertPurchaseRejection(t, req, "illegal-offer", contracts.PriceBelowMinimum)
			return purchaseTextCall(t, "illegal-publication", marketplace.AgentOfferText)
		},
		func(req llm.Request) llm.ToolCall {
			assertPurchaseRejection(t, req, "illegal-publication", contracts.InvalidArgument)
			if f.loadLead(t).Offer != nil || len(f.history(t)) != 0 {
				t.Fatal("rejected offer was retained or published")
			}
			return purchaseCall("corrected-offer", "make_offer", `{"item_id":"item_thinkpad","price":5400000}`)
		},
		func(llm.Request) llm.ToolCall {
			return purchaseTextCall(t, "corrected-publication", marketplace.AgentOfferText)
		},
	}}
	f.run(t, f.runner(t, model), runtime.Event{ID: "below-floor-turn", LeadID: f.lead.ID, Trigger: runtime.TriggerLeadDiscovered}, nil)
	model.assertConsumed()
	lead := f.loadLead(t)
	history := f.history(t)
	if lead.Offer == nil || lead.Offer.PriceIDR != 5_400_000 || lead.Checkout != nil || lead.State != domain.LeadActive || len(history) != 1 || history[0].Text != "I can offer this item for Rp 5400000." {
		t.Fatal("correction did not produce exactly one legal offer publication")
	}
	f.assertStock(t, 2, 0, 2)
}

const purchaseOrigin = "http://checkout.test"

type purchaseFixture struct {
	db           *sql.DB
	commerce     *commerce.Service
	lead         domain.LeadRecord
	conversation *marketplacetest.Conversation
	thread       domain.ThreadRef
	now          func() time.Time
	advance      func()
}

func newPurchaseFixture(t *testing.T) *purchaseFixture {
	t.Helper()
	ctx := t.Context()
	db, err := mockpos.OpenSQLite(ctx, filepath.Join(t.TempDir(), "purchase.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	currentTime := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return currentTime }
	// ThreadHistory orders timestamp ties by opaque ID. Give each conversation
	// event a distinct deterministic time rather than depending on ID ordering.
	advance := func() { currentTime = currentTime.Add(time.Second) }
	sequence := 0
	service, err := commerce.New(db, commerce.Options{Now: now, CheckoutOrigin: purchaseOrigin, NewID: func() string {
		sequence++
		return fmt.Sprintf("purchase-%d", sequence)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Inventory().SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	lead, created, err := service.CreateLead(ctx, commerce.CreateLeadInput{SellerID: mockpos.DemoSellerID, Platform: "mock", PostID: "purchase-post", BuyerID: "purchase-buyer", ThreadID: "purchase-thread", ItemID: "item_thinkpad",
		Intent: domain.Intent{IsBuyingRequest: true, Confidence: .95, ItemQuery: "coding laptop", Qty: 1, Location: domain.Location{City: "Jakarta"}}, MatchReason: "16GB coding laptop"})
	if err != nil || !created {
		t.Fatalf("create purchase lead: created=%v err=%v", created, err)
	}
	conversation, err := marketplacetest.NewConversation(ctx, domain.Post{PostRef: domain.PostRef{Platform: lead.Platform, ExternalPostID: lead.PostID}, BuyerID: lead.BuyerID, ThreadID: lead.ThreadID, Text: "Looking for a used laptop", CreatedAt: now()}, now)
	if err != nil {
		t.Fatal(err)
	}
	return &purchaseFixture{db: db, commerce: service, lead: lead, conversation: conversation, thread: domain.ThreadRef{Platform: lead.Platform, ThreadID: lead.ThreadID}, now: now, advance: advance}
}

func (f *purchaseFixture) runner(t *testing.T, model runtime.TurnModel) *runtime.TurnRunner {
	t.Helper()
	sender, err := marketplace.NewAgentSender(f.commerce, f.conversation.Threads, f.conversation.Threads, purchaseOrigin, f.now)
	if err != nil {
		t.Fatal(err)
	}
	factory := func(_ context.Context, loaded *runtime.LeadContext, metadata runtime.TurnMetadata) ([]runtime.Tool, error) {
		tools, err := agenttools.NewCommerceTools(f.commerce, &loaded.Lead, metadata.ActionKey)
		if err != nil {
			return nil, err
		}
		send, err := agenttools.NewSendMessageTool(sender.SendAgentMessage, &loaded.Lead, metadata.ActionKey, agenttools.SendMessageOptions{CheckoutOrigin: purchaseOrigin, Now: f.now})
		return append(tools, send), err
	}
	runner, err := runtime.NewTurnRunner(f.commerce, model, factory, runtime.LoopLimits{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func (f *purchaseFixture) run(t *testing.T, runner *runtime.TurnRunner, event runtime.Event, history []llm.ChatMessage) {
	t.Helper()
	result, err := runner.RunTurn(t.Context(), runtime.TurnInput{Event: event, Instructions: "Use the conversation tools and approved message templates.", TriggerInstruction: "Process the current event.", History: history})
	if err != nil || !result.Stopped {
		t.Fatalf("purchase turn failed: stopped=%v err=%v", result.Stopped, err)
	}
}

func (f *purchaseFixture) loadLead(t *testing.T) domain.LeadRecord {
	t.Helper()
	lead, err := f.commerce.GetLead(t.Context(), f.lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	return lead
}

func (f *purchaseFixture) history(t *testing.T) []domain.Message {
	t.Helper()
	history, err := f.conversation.Threads.ThreadHistory(t.Context(), f.thread)
	if err != nil {
		t.Fatal(err)
	}
	return history
}

func (f *purchaseFixture) assertStock(t *testing.T, physical, reserved, available int) {
	t.Helper()
	item, err := f.commerce.Inventory().Get(t.Context(), f.lead.SellerID, f.lead.ItemID)
	if err != nil || item.PhysicalStock != physical || item.ReservedStock != reserved || item.AvailableStock != available {
		t.Fatalf("stock: physical=%d reserved=%d available=%d err=%v", item.PhysicalStock, item.ReservedStock, item.AvailableStock, err)
	}
}

type purchaseModel struct {
	t     *testing.T
	steps []func(llm.Request) llm.ToolCall
	next  int
}

func (m *purchaseModel) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	if m.next >= len(m.steps) {
		return llm.Response{}, fmt.Errorf("purchase script exhausted")
	}
	step := m.steps[m.next]
	m.next++
	return llm.Response{ToolCalls: []llm.ToolCall{step(req)}, Continuation: &llm.Continuation{}}, nil
}

func (m *purchaseModel) assertConsumed() {
	m.t.Helper()
	if m.next != len(m.steps) {
		m.t.Fatalf("purchase script completed %d/%d steps", m.next, len(m.steps))
	}
}

func purchaseCall(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
}

func purchaseTextCall(t *testing.T, id, text string) llm.ToolCall {
	t.Helper()
	args, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		t.Fatal(err)
	}
	return purchaseCall(id, "send_message", string(args))
}

func assertPurchaseRejection(t *testing.T, req llm.Request, callID string, code contracts.Code) {
	t.Helper()
	var output struct{ Error contracts.Code }
	if len(req.ToolResults) != 1 || req.ToolResults[0].CallID != callID || json.Unmarshal([]byte(req.ToolResults[0].Output), &output) != nil || output.Error != code {
		t.Fatalf("expected %s rejection for %s, got %+v", code, callID, req.ToolResults)
	}
}
