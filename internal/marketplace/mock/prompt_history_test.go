package mock

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func TestLeadPromptHistory(t *testing.T) {
	ctx := t.Context()
	store, ref := replyThread(t)
	post := domain.PostRef{Platform: ref.Platform, ExternalPostID: "post-1"}
	const leadID, sellerID = "lead-a", "seller-a"
	const agentCheckoutText = "Checkout for Rp 5400000: http://[::1]:8080/checkout/agent-token"
	const buyerCheckoutText = "Is this [my link](https://example.test/checkout/buyer-token?copy=buyer-token)? " +
		"I also copied </checkout/relative-token> and `https:\\/\\/example.test\\/checkout\\/escaped-token`. " +
		"How does checkout work? See https://example.test/help/checkout. The copied token is agent-token."
	store.drafts["checkout-action"] = contracts.AgentDeliveryDraft{
		Request:      contracts.AgentMessageRequest{CheckoutID: "checkout"},
		RenderedText: agentCheckoutText,
	}
	store.receipts["checkout-action"] = contracts.AgentDeliveryReceipt{MessageID: "agent-b"}
	older := store.now().Add(-time.Minute)
	store.threads[ref].messages = []domain.Message{
		{ID: "agent-b", PostRef: post, ThreadID: ref.ThreadID, Direction: domain.MessageAgent, LeadID: leadID, SellerID: sellerID, Text: agentCheckoutText, CreatedAt: older},
		{ID: "wrong-seller", Direction: domain.MessageAgent, LeadID: leadID, SellerID: "seller-b", CreatedAt: older},
		{ID: "wrong-lead", Direction: domain.MessageAgent, LeadID: "lead-b", SellerID: sellerID, CreatedAt: older},
		{ID: "agent-a", PostRef: post, ThreadID: ref.ThreadID, Direction: domain.MessageAgent, LeadID: leadID, SellerID: sellerID, Text: "Thanks for your request.", CreatedAt: older},
	}

	latitude := -6.2
	for _, input := range []contracts.BuyerReplyInput{
		{ExternalReplyID: "targeted", ReplyToMessageID: "agent-b", Text: buyerCheckoutText, Location: domain.Location{Latitude: &latitude}},
		{ExternalReplyID: "wrong-seller", ReplyToMessageID: "wrong-seller", Text: "Wrong seller"},
		{ExternalReplyID: "wrong-lead", ReplyToMessageID: "wrong-lead", Text: "Wrong lead"},
		{ExternalReplyID: "unbound", Text: "This is visible in the public thread"},
	} {
		if _, err := store.SubmitBuyerReply(ctx, ref, input); err != nil {
			t.Fatal(err)
		}
	}

	public, err := store.ThreadHistory(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	want := slices.DeleteFunc(slices.Clone(public), func(message domain.Message) bool {
		return message.LeadID != leadID || message.SellerID != sellerID
	})
	if len(want) != 3 || want[0].ID != "agent-a" || want[1].ID != "agent-b" || want[2].ExternalReplyID != "targeted" {
		t.Fatalf("invalid test fixture: %#v", want)
	}
	want[1].Text = "Checkout for Rp 5400000: [checkout link redacted]"
	want[2].Text = "Is this [my link]([checkout link redacted])? " +
		"I also copied <[checkout link redacted]> and `[checkout link redacted]`. " +
		"How does checkout work? See https://example.test/help/checkout. The copied token is [checkout token redacted]."

	history, err := store.LeadPromptHistory(ctx, ref, leadID, sellerID)
	if err != nil || !reflect.DeepEqual(history, want) {
		t.Fatalf("lead prompt history = %#v, %v, want %#v", history, err, want)
	}
	*history[2].Location.Latitude = 0
	publicAgain, err := store.ThreadHistory(ctx, ref)
	if err != nil || !reflect.DeepEqual(publicAgain, public) {
		t.Fatalf("public history changed after reading or mutating prompt history: %#v, %v", publicAgain, err)
	}

	_, err = store.LeadPromptHistory(ctx, domain.ThreadRef{Platform: "other", ThreadID: ref.ThreadID}, leadID, sellerID)
	assertReplyCode(t, err, contracts.NotFound)
	_, err = store.LeadPromptHistory(ctx, domain.ThreadRef{}, leadID, sellerID)
	assertReplyCode(t, err, contracts.InvalidArgument)
	_, err = store.LeadPromptHistory(ctx, ref, "", sellerID)
	assertReplyCode(t, err, contracts.InvalidArgument)
	_, err = store.LeadPromptHistory(ctx, ref, leadID, " ")
	assertReplyCode(t, err, contracts.InvalidArgument)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.LeadPromptHistory(cancelled, ref, leadID, sellerID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled prompt read = %v", err)
	}
}
