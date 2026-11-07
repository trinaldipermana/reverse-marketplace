package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/mockpos"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/marketplacetest"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
	agenttools "github.com/trinaldirizki/reverse-marketplace/internal/runtime/tools"
	"github.com/trinaldirizki/reverse-marketplace/migrations"
)

func TestP1AssemblyRunsInitialAndExplicitReply(t *testing.T) {
	f := newAssemblyFixture(t)
	calls := 0
	f.config.Model = assemblyModel(func(req llm.Request) (llm.Response, error) {
		calls++
		if calls == 1 {
			names := []string{}
			for _, tool := range req.Tools {
				names = append(names, tool.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, []string{"close_lead", "create_checkout", "escalate_to_seller", "get_item", "make_offer", "send_message"}) {
				t.Fatalf("tools=%v", names)
			}
			return assemblyCall("offer", "make_offer", `{"item_id":"item_thinkpad","price":5400000}`), nil
		}
		text := marketplace.AgentOfferText
		if calls == 3 {
			text = marketplace.AgentAcknowledgmentText
		}
		raw, _ := json.Marshal(map[string]string{"text": text})
		return assemblyCall(fmt.Sprint(calls), "send_message", string(raw)), nil
	})
	snapshots := 0
	f.config.Classify = func(_ context.Context, input agentruntime.TurnInput) (agentruntime.ScopeDecision, error) {
		snapshots++
		last := input.History[len(input.History)-1]
		expected := f.post.Text
		if snapshots == 2 {
			expected = "Is this still available?"
		}
		if last.Role != "user" || last.Content != expected {
			t.Fatalf("scope received wrong buyer request: %+v", last)
		}
		return agentruntime.ScopeInScope, nil
	}
	assembled, err := NewP1Runtime(f.config)
	if err != nil {
		t.Fatal(err)
	}
	initial := agentruntime.Event{ID: "initial", LeadID: f.lead.ID, Trigger: agentruntime.TriggerLeadDiscovered}
	if err := assembled.runEvent(t.Context(), initial); err != nil {
		t.Fatal(err)
	}
	history, err := f.conversation.Threads.ThreadHistory(t.Context(), f.thread)
	if err != nil || len(history) != 1 || history[0].Text != "I can offer this item for Rp 5400000." {
		t.Fatalf("initial publication=%+v err=%v", history, err)
	}
	reply, err := f.conversation.Threads.SubmitBuyerReply(t.Context(), f.thread, contracts.BuyerReplyInput{ExternalReplyID: "buyer-one", Text: "Is this still available?", ReplyToMessageID: history[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := assembled.AcceptBuyerReply(t.Context(), reply.Event)
	if err != nil || ack != contracts.BuyerReplyAcceptedVolatile {
		t.Fatalf("ack=%s err=%v", ack, err)
	}
	result, ok := assembled.Result(reply.Event.ID)
	if !ok || result.Status != agentruntime.EventQueued || result.Event.MessageID != reply.Event.Message.ID {
		t.Fatalf("queued result=%+v found=%v", result, ok)
	}
	// The test loader is an explicit safe fixture, not public thread projection.
	f.history = P1ReplyHistory{CurrentMessageID: reply.Event.Message.ID, Messages: []llm.ChatMessage{{Role: "user", Content: reply.Event.Message.Text}}}
	if err := assembled.runEvent(t.Context(), result.Event); err != nil {
		t.Fatal(err)
	}
	if snapshots != 2 || calls != 3 {
		t.Fatalf("snapshots=%d model calls=%d", snapshots, calls)
	}
	history, _ = f.conversation.Threads.ThreadHistory(t.Context(), f.thread)
	if len(history) != 3 || history[2].Direction != domain.MessageAgent {
		t.Fatalf("reply publication=%+v", history)
	}
}

func TestP1AssemblyWorkerLifecycle(t *testing.T) {
	f := newAssemblyFixture(t)
	f.config.Classify = func(context.Context, agentruntime.TurnInput) (agentruntime.ScopeDecision, error) {
		return agentruntime.ScopeInScope, nil
	}
	f.config.Model = assemblyModel(func(llm.Request) (llm.Response, error) {
		raw, _ := json.Marshal(map[string]string{"text": marketplace.AgentAcknowledgmentText})
		return assemblyCall("ack", "send_message", string(raw)), nil
	})
	assembled, err := NewP1Runtime(f.config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- assembled.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("worker shutdown=%v", err)
		}
	}()
	if err := assembled.OnLeadDiscovered(t.Context(), "worker-initial", f.lead.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		result, ok := assembled.Result("worker-initial")
		if ok && result.Status == agentruntime.EventSucceeded {
			break
		}
		if ok && result.Status == agentruntime.EventFailed {
			t.Fatalf("worker failed: %v", result.Err)
		}
		select {
		case <-deadline.C:
			t.Fatal("worker did not complete")
		case <-tick.C:
		}
	}
	history, err := f.conversation.Threads.ThreadHistory(t.Context(), f.thread)
	if err != nil || len(history) != 1 || history[0].Text != marketplace.AgentAcknowledgmentText {
		t.Fatalf("worker publication=%+v err=%v", history, err)
	}
	if err := assembled.OnLeadDiscovered(t.Context(), "worker-initial", f.lead.ID); err != nil {
		t.Fatal(err)
	}
	history, _ = f.conversation.Threads.ThreadHistory(t.Context(), f.thread)
	if len(history) != 1 {
		t.Fatal("duplicate initial event published twice")
	}
}

func TestP1AssemblyBlocksUnassignedMismatchedAndMissingScope(t *testing.T) {
	f := newAssemblyFixture(t)
	modelCalls := 0
	f.config.Model = assemblyModel(func(llm.Request) (llm.Response, error) { modelCalls++; return llm.Response{}, nil })
	assembled, err := NewP1Runtime(f.config)
	if err != nil {
		t.Fatal(err)
	}
	message := domain.Message{ID: "message", PostRef: f.post.PostRef, BuyerID: f.lead.BuyerID, ThreadID: f.lead.ThreadID, Direction: domain.MessageBuyer, Text: "Hello", CreatedAt: f.post.CreatedAt}
	event := contracts.BuyerReplyEvent{ID: "reply", Message: message}
	if _, err := assembled.AcceptBuyerReply(t.Context(), event); !errors.Is(err, ErrP1ReplyUnassigned) {
		t.Fatalf("unassigned=%v", err)
	}
	event.Message.LeadID = f.lead.ID
	event.Message.SellerID = "another-seller"
	if _, err := assembled.AcceptBuyerReply(t.Context(), event); !errors.Is(err, ErrP1ReplyScope) {
		t.Fatalf("wrong seller=%v", err)
	}
	if _, ok := assembled.Result(event.ID); ok {
		t.Fatal("invalid reply enqueued")
	}
	if err := assembled.runEvent(t.Context(), agentruntime.Event{ID: "initial", LeadID: f.lead.ID, Trigger: agentruntime.TriggerLeadDiscovered}); !errors.Is(err, agentruntime.ErrScopeUnavailable) {
		t.Fatalf("missing classifier=%v", err)
	}
	if modelCalls != 0 {
		t.Fatalf("model called %d times", modelCalls)
	}
	lead, _ := f.service.GetLead(t.Context(), f.lead.ID)
	history, _ := f.conversation.Threads.ThreadHistory(t.Context(), f.thread)
	if lead.State != domain.LeadActive || lead.Offer != nil || len(history) != 0 {
		t.Fatal("blocked event acted")
	}
}

func TestP1AssemblyRejectsStaleHistoryAndCanonicalMismatch(t *testing.T) {
	f := newAssemblyFixture(t)
	f.config.Classify = func(context.Context, agentruntime.TurnInput) (agentruntime.ScopeDecision, error) {
		t.Fatal("scope called on invalid snapshot")
		return "", nil
	}
	assembled, err := NewP1Runtime(f.config)
	if err != nil {
		t.Fatal(err)
	}
	event := agentruntime.Event{ID: "reply", LeadID: f.lead.ID, Trigger: agentruntime.TriggerBuyerReply, MessageID: "current"}
	f.history = P1ReplyHistory{CurrentMessageID: "old", Messages: []llm.ChatMessage{{Role: "user", Content: "old"}}}
	if err := assembled.runEvent(t.Context(), event); !errors.Is(err, ErrP1ReplyHistory) {
		t.Fatalf("stale snapshot=%v", err)
	}
	f.config.Posts = assemblyPostLookup(func(context.Context, domain.PostRef) (domain.Post, error) {
		p := f.post
		p.BuyerID = "other"
		return p, nil
	})
	assembled, err = NewP1Runtime(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := assembled.runEvent(t.Context(), agentruntime.Event{ID: "initial", LeadID: f.lead.ID, Trigger: agentruntime.TriggerLeadDiscovered}); !errors.Is(err, ErrP1ReplyScope) {
		t.Fatalf("canonical mismatch=%v", err)
	}
}

type assemblyModel func(llm.Request) (llm.Response, error)

func (m assemblyModel) Generate(_ context.Context, r llm.Request) (llm.Response, error) { return m(r) }
func assemblyCall(id, name, args string) llm.Response {
	return llm.Response{ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: json.RawMessage(args)}}, Continuation: &llm.Continuation{}}
}

type assemblyPostLookup func(context.Context, domain.PostRef) (domain.Post, error)

func (p assemblyPostLookup) GetPost(c context.Context, r domain.PostRef) (domain.Post, error) {
	return p(c, r)
}

type assemblyFixture struct {
	config       P1RuntimeConfig
	service      *commerce.Service
	lead         domain.LeadRecord
	post         domain.Post
	thread       domain.ThreadRef
	conversation *marketplacetest.Conversation
	history      P1ReplyHistory
}

func newAssemblyFixture(t *testing.T) *assemblyFixture {
	t.Helper()
	db, err := mockpos.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "assembly.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.Apply(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	current := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { current = current.Add(time.Second); return current }
	seq := 0
	service, err := commerce.New(db, commerce.Options{Now: now, CheckoutOrigin: "http://checkout.test", NewID: func() string { seq++; return fmt.Sprintf("assembly-%d", seq) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Inventory().SeedDemo(t.Context()); err != nil {
		t.Fatal(err)
	}
	lead, _, err := service.CreateLead(t.Context(), commerce.CreateLeadInput{SellerID: mockpos.DemoSellerID, Platform: "mock", PostID: "post", BuyerID: "buyer", ThreadID: "thread", ItemID: "item_thinkpad", Intent: domain.Intent{IsBuyingRequest: true, Confidence: .95, ItemQuery: "coding laptop", Qty: 1}, MatchReason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	post := domain.Post{PostRef: domain.PostRef{Platform: lead.Platform, ExternalPostID: lead.PostID}, BuyerID: lead.BuyerID, ThreadID: lead.ThreadID, Text: "Looking for a laptop for coding", CreatedAt: now()}
	conversation, err := marketplacetest.NewConversation(t.Context(), post, now)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := marketplace.NewAgentSender(service, conversation.Threads, conversation.Threads, "http://checkout.test", now)
	if err != nil {
		t.Fatal(err)
	}
	f := &assemblyFixture{service: service, lead: lead, post: post, thread: domain.ThreadRef{Platform: lead.Platform, ThreadID: lead.ThreadID}, conversation: conversation}
	f.config = P1RuntimeConfig{Commerce: service, Posts: conversation.Posts, Model: assemblyModel(func(llm.Request) (llm.Response, error) { return llm.Response{}, nil }), Send: sender.SendAgentMessage, SendOptions: agenttools.SendMessageOptions{CheckoutOrigin: "http://checkout.test", Now: now}, QueueCapacity: 8, Limits: agentruntime.LoopLimits{Timeout: 5 * time.Second}, Instructions: func(context.Context, domain.LeadRecord) (string, error) {
		return "Use only the approved seller tools and templates.", nil
	}, ReplyHistory: func(context.Context, domain.LeadRecord, agentruntime.Event) (P1ReplyHistory, error) {
		return f.history, nil
	}}
	return f
}
