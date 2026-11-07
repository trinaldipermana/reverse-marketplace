package marketplace_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
)

const partnerTestURL = "https://partner.example/listing/t480?ref=demo%2Fbuyers&order=1"

func TestFixedPriceAgentDelivery(t *testing.T) {
	f := newSendFixture(t)
	f.lead.Offer = nil
	f.item.ListPriceIDR = 5_800_000
	f.item.Attributes = map[string]string{"partner_listing_url": partnerTestURL}
	sender := fixedPriceSender(t, f, f.store)

	f.request.Text = "I found a match at a fixed price of {listing_price}."
	outreach, err := sender.SendAgentMessage(t.Context(), f.request)
	if err != nil || outreach.Status != contracts.AgentDeliveryDelivered {
		t.Fatalf("outreach = %#v, %v", outreach, err)
	}
	f.request.Text = "Here is the partner listing: {partner_listing_link}"
	f.request.ActionID = "buyer-listing-request"
	f.now = f.now.Add(time.Second)
	link, err := sender.SendAgentMessage(t.Context(), f.request)
	if err != nil || link.Status != contracts.AgentDeliveryDelivered || link.Receipt.ActionID != f.request.ActionID {
		t.Fatalf("link = %#v, %v", link, err)
	}
	delete(f.item.Attributes, "partner_listing_url")
	replay, err := sender.SendAgentMessage(t.Context(), f.request)
	if err != nil || replay != link {
		t.Fatalf("retry = %#v, %v; want %#v", replay, err, link)
	}

	f.item.Attributes["partner_listing_url"] = partnerTestURL
	f.request.ActionID = "duplicate-reply-with-new-action"
	_, err = sender.SendAgentMessage(t.Context(), f.request)
	assertSendCode(t, err, contracts.InvalidState)

	history := f.history(t)
	if len(history) != 2 || history[0].Text != "I found a match at a fixed price of Rp 5800000." ||
		history[1].ID != link.Receipt.MessageID || history[1].Text != "Here is the partner listing: "+partnerTestURL {
		t.Fatalf("unexpected thread history: %#v", history)
	}
}

func TestFixedPriceAgentDeliveryRejectsUnsafeTerms(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sendFixture)
		want   contracts.Code
	}{
		{"missing listing", func(f *sendFixture) { delete(f.item.Attributes, "partner_listing_url") }, contracts.InvalidArgument},
		{"unapproved host", func(f *sendFixture) {
			f.item.Attributes["partner_listing_url"] = "https://partner.example.evil/listing"
		}, contracts.InvalidArgument},
		{"unescaped whitespace", func(f *sendFixture) {
			f.item.Attributes["partner_listing_url"] = "https://partner.example/listing name"
		}, contracts.InvalidArgument},
		{"insecure destination", func(f *sendFixture) { f.item.Attributes["partner_listing_url"] = "http://partner.example/listing" }, contracts.InvalidArgument},
		{"another seller", func(f *sendFixture) { f.item.SellerID = "other" }, contracts.InvalidState},
		{"another item", func(f *sendFixture) { f.item.ID = "other" }, contracts.InvalidState},
		{"model supplied URL", func(f *sendFixture) { f.request.Text = "Here is the partner listing: " + partnerTestURL }, contracts.InvalidArgument},
		{"stored offer template", func(f *sendFixture) { f.request.Text = marketplace.AgentOfferText }, contracts.InvalidArgument},
		{"internal checkout template", func(f *sendFixture) { f.reserve() }, contracts.InvalidArgument},
		{"reserved lead", func(f *sendFixture) { f.lead.State = domain.LeadReserved }, contracts.InvalidState},
		{"checkout metadata", func(f *sendFixture) { f.request.CheckoutID = "checkout" }, contracts.InvalidState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSendFixture(t)
			f.item.Attributes = map[string]string{"partner_listing_url": partnerTestURL}
			f.request.Text = "Here is the partner listing: {partner_listing_link}"
			tc.change(f)
			result, err := fixedPriceSender(t, f, f.store).SendAgentMessage(t.Context(), f.request)
			assertSendCode(t, err, tc.want)
			if result.Status != contracts.AgentDeliveryRejected || len(f.history(t)) != 0 {
				t.Fatalf("unexpected publication: %#v", result)
			}
		})
	}
}

func TestFixedPriceAgentDeliveryReconcilesUnconfirmedPublication(t *testing.T) {
	f := newSendFixture(t)
	f.lead.Offer = nil
	f.item.Attributes = map[string]string{"partner_listing_url": partnerTestURL}
	f.request.Text = "Here is the partner listing: {partner_listing_link}"

	lost := errors.New("lost publisher response")
	var receipt contracts.AgentDeliveryReceipt
	publisher := faultPublisher{AgentMessagePublisher: f.store, send: func(ctx context.Context, publication contracts.AgentPublication) (contracts.AgentDeliveryReceipt, error) {
		var err error
		receipt, err = f.store.PublishAgentMessage(ctx, publication)
		if err != nil {
			t.Fatal(err)
		}
		return contracts.AgentDeliveryReceipt{}, lost
	}}
	pending, err := fixedPriceSender(t, f, publisher).SendAgentMessage(t.Context(), f.request)
	if !errors.Is(err, lost) || pending.Status != contracts.AgentDeliveryPending || pending.Receipt != (contracts.AgentDeliveryReceipt{}) {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
	result, err := fixedPriceSender(t, f, f.store).SendAgentMessage(t.Context(), f.request)
	if err != nil || result.Status != contracts.AgentDeliveryDelivered || result.Receipt != receipt || len(f.history(t)) != 1 {
		t.Fatalf("reconciled send = %#v, %v", result, err)
	}
}

func TestFixedPriceAgentDeliveryRequiresMatchingReceipt(t *testing.T) {
	f := newSendFixture(t)
	f.item.Attributes = map[string]string{"partner_listing_url": partnerTestURL}
	f.request.Text = "Here is the partner listing: {partner_listing_link}"
	publisher := faultPublisher{AgentMessagePublisher: f.store, send: func(context.Context, contracts.AgentPublication) (contracts.AgentDeliveryReceipt, error) {
		return contracts.AgentDeliveryReceipt{ActionID: "another-action", MessageID: "message", PublishedAt: f.now}, nil
	}}
	pending, err := fixedPriceSender(t, f, publisher).SendAgentMessage(t.Context(), f.request)
	if err == nil || pending.Status != contracts.AgentDeliveryPending || pending.Receipt != (contracts.AgentDeliveryReceipt{}) {
		t.Fatalf("unconfirmed send = %#v, %v", pending, err)
	}
	result, err := fixedPriceSender(t, f, f.store).SendAgentMessage(t.Context(), f.request)
	if err != nil || result.Status != contracts.AgentDeliveryDelivered || len(f.history(t)) != 1 {
		t.Fatalf("reconciled send = %#v, %v", result, err)
	}
}

func fixedPriceSender(t *testing.T, f *sendFixture, publisher contracts.AgentMessagePublisher) *marketplace.AgentSender {
	t.Helper()
	sender, err := marketplace.NewFixedPriceAgentSender(f.commerce, f.store, publisher, []string{"partner.example"}, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	return sender
}
