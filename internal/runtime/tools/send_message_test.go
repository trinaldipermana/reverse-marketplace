package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/commercefake"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/marketplacetest"
)

func sendFixture() (domain.LeadRecord, time.Time, SendMessageOptions) {
	lead, now := renderFixture()
	lead.State, lead.Checkout = domain.LeadActive, nil
	lead.Platform, lead.ThreadID, lead.BuyerID, lead.PostID = "mock", "thread", "buyer", "post"
	return lead, now, SendMessageOptions{CheckoutOrigin: "https://shop.example", Channel: contracts.AgentPublicReply, Now: func() time.Time { return now }}
}

func sendArgs(text string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"text": text})
	return raw
}

func sendKey(context.Context, string) (string, error) { return "event/call", nil }

func deliveredReceipt(req contracts.AgentMessageRequest, now time.Time) contracts.AgentDeliveryResult {
	return contracts.AgentDeliveryResult{Status: contracts.AgentDeliveryDelivered, Receipt: contracts.AgentDeliveryReceipt{ActionID: req.ActionID, MessageID: "opaque-message", PublishedAt: now}}
}

func TestSendMessageUsesActualP4Sender(t *testing.T) {
	lead, now, options := sendFixture()
	fake := commercefake.NewSeeded()
	fake.GetLeadFunc = func(context.Context, string) (domain.LeadRecord, error) { return lead, nil }
	fake.GetItemFunc = func(context.Context, string, string) (domain.Item, error) {
		return domain.Item{ID: lead.ItemID, SellerID: lead.SellerID, Active: true, ListPriceIDR: 5_800_000, AvailableStock: 1}, nil
	}
	fake.GetRulesFunc = func(context.Context, string, string) (domain.Rules, error) {
		return domain.Rules{FloorPriceIDR: 5_400_000, MaxDiscountBPS: 1000, MessageCap: 10, RateLimitPerMinute: 20}, nil
	}
	post := domain.Post{PostRef: domain.PostRef{Platform: lead.Platform, ExternalPostID: lead.PostID}, BuyerID: lead.BuyerID, ThreadID: lead.ThreadID, Text: "Need a laptop", CreatedAt: now}
	fixture, err := marketplacetest.NewConversation(t.Context(), post, options.Now)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := marketplace.NewAgentSender(fake, fixture.Threads, fixture.Threads, options.CheckoutOrigin, options.Now)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewSendMessageTool(sender.SendAgentMessage, &lead, sendKey, options)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := tool.Run(t.Context(), sendArgs(marketplace.AgentOfferText))
	if err != nil || !outcome.Stop || outcome.Output != `{"status":"delivered"}` {
		t.Fatalf("send outcome=%+v error=%v", outcome, err)
	}
	history, err := fixture.Threads.ThreadHistory(t.Context(), domain.ThreadRef{Platform: lead.Platform, ThreadID: lead.ThreadID})
	if err != nil || len(history) != 1 || history[0].Text != "I can offer this item for Rp 5600000." {
		t.Fatalf("P4 did not render the stored offer: %v", err)
	}
}

func TestSendMessageBindsOnlyCurrentTurnCheckout(t *testing.T) {
	for _, state := range []string{"new", "existing", "expired"} {
		t.Run(state, func(t *testing.T) {
			lead, now, options := sendFixture()
			reserved, _ := renderFixture()
			if state == "existing" {
				lead.State, lead.Checkout = domain.LeadReserved, reserved.Checkout
			}
			calls := 0
			send := func(_ context.Context, req contracts.AgentMessageRequest) (contracts.AgentDeliveryResult, error) {
				calls++
				if req.ActionID != "event/call" || req.LeadID != lead.ID || req.SellerID != lead.SellerID || req.Thread != (domain.ThreadRef{Platform: lead.Platform, ThreadID: lead.ThreadID}) || req.Channel != contracts.AgentPublicReply || req.CheckoutID != lead.Checkout.ID || req.Text != marketplace.AgentCheckoutText {
					t.Fatal("incorrect trusted delivery request")
				}
				if strings.Contains(req.Text, "private-token") {
					t.Fatal("rendered bearer URL sent as model text")
				}
				return deliveredReceipt(req, now), nil
			}
			tool, err := NewSendMessageTool(send, &lead, sendKey, options)
			if err != nil {
				t.Fatal(err)
			}
			lead.State, lead.Checkout = domain.LeadReserved, reserved.Checkout
			if state == "expired" {
				lead.Checkout.ExpiresAt = now
			}
			out, err := tool.Run(t.Context(), sendArgs(marketplace.AgentCheckoutText))
			if state == "new" {
				if err != nil || !out.Stop || calls != 1 {
					t.Fatalf("new checkout send=%+v calls=%d error=%v", out, calls, err)
				}
			} else if calls != 0 || out.Stop || err != nil {
				t.Fatalf("stale/expired checkout reached delivery: calls=%d outcome=%+v error=%v", calls, out, err)
			}
			if strings.Contains(out.Output, "private-token") {
				t.Fatal("tool result leaked checkout token")
			}
		})
	}
}

func TestSendMessageDistinguishesDeliveryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     contracts.AgentDeliveryStatus
		code       contracts.Code
		receipt    string
		wantError  error
		wantOutput string
	}{
		{"rejected", contracts.AgentDeliveryRejected, contracts.InvalidArgument, "", nil, `{"error":"INVALID_ARGUMENT"}`},
		{"dependency", "", contracts.DependencyUnavailable, "", ErrSendMessageFailed, ""},
		{"pending", contracts.AgentDeliveryPending, contracts.DependencyUnavailable, "", ErrSendMessagePending, `{"status":"pending"}`},
		{"missing receipt", contracts.AgentDeliveryDelivered, "", "missing", ErrSendMessageFailed, ""},
		{"wrong receipt", contracts.AgentDeliveryDelivered, "", "wrong", ErrSendMessageFailed, ""},
		{"rejected with receipt", contracts.AgentDeliveryRejected, contracts.InvalidArgument, "present", ErrSendMessageFailed, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lead, now, options := sendFixture()
			calls := 0
			send := func(_ context.Context, req contracts.AgentMessageRequest) (contracts.AgentDeliveryResult, error) {
				calls++
				result := contracts.AgentDeliveryResult{Status: tc.status}
				if tc.receipt == "wrong" {
					result = deliveredReceipt(req, now)
					result.Receipt.ActionID = "other"
				}
				if tc.receipt == "present" {
					result.Receipt = deliveredReceipt(req, now).Receipt
				}
				if tc.code != "" {
					return result, &contracts.Error{Code: tc.code, Message: "private-token https://secret.invalid/checkout/private-token", Retryable: tc.code == contracts.DependencyUnavailable}
				}
				return result, nil
			}
			tool, err := NewSendMessageTool(send, &lead, sendKey, options)
			if err != nil {
				t.Fatal(err)
			}
			out, err := tool.Run(t.Context(), sendArgs(marketplace.AgentAcknowledgmentText))
			if !errors.Is(err, tc.wantError) || out.Output != tc.wantOutput || out.Stop || calls != 1 {
				t.Fatalf("outcome=%+v calls=%d error=%v", out, calls, err)
			}
			if err != nil && (strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "secret.invalid")) {
				t.Fatal("sender error leaked private input")
			}
			if tc.status == contracts.AgentDeliveryPending {
				_, again := tool.Run(t.Context(), sendArgs(marketplace.AgentAcknowledgmentText))
				if !errors.Is(again, ErrSendMessagePending) || calls != 1 {
					t.Fatal("uncertain publication triggered another send")
				}
			}
		})
	}
}

func TestSendMessageRejectsInvalidModelArguments(t *testing.T) {
	for _, raw := range []string{`{`, `{"text":null}`, `{"Text":"hello"}`, `{"text":"hello","seller_id":"other"}`, `{"text":""}`, `{"text":"Pay https://evil.example"}`} {
		lead, _, options := sendFixture()
		tool, err := NewSendMessageTool(func(context.Context, contracts.AgentMessageRequest) (contracts.AgentDeliveryResult, error) {
			t.Fatal("invalid input reached sender")
			return contracts.AgentDeliveryResult{}, nil
		}, &lead, sendKey, options)
		if err != nil {
			t.Fatal(err)
		}
		out, err := tool.Run(t.Context(), json.RawMessage(raw))
		if err != nil || out.Stop || out.Output != `{"error":"INVALID_ARGUMENT"}` {
			t.Fatalf("invalid arguments outcome=%+v error=%v", out, err)
		}
	}
}
