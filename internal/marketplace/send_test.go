package marketplace_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce/commercefake"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/marketplacetest"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/mock"
)

const checkoutURL = "https://checkout.example/checkout/stored-opaque_token"

func TestAgentSendGuardrails(t *testing.T) {
	t.Run("stored offer renders and routes to the lead", func(t *testing.T) {
		f := newSendFixture(t)
		f.item.ListPriceIDR, f.rules.FloorPriceIDR = 5_999_999, 1
		result, err := f.sender.SendAgentMessage(t.Context(), f.request)
		if err != nil || result.Status != contracts.AgentDeliveryDelivered {
			t.Fatalf("send = %#v, %v", result, err)
		}
		history := f.history(t)
		if len(history) != 1 || history[0].ID != result.Receipt.MessageID || history[0].Text != "I can offer this item for Rp 5400000." ||
			history[0].Direction != domain.MessageAgent || history[0].LeadID != f.lead.ID || history[0].SellerID != f.lead.SellerID {
			t.Fatalf("history = %#v", history)
		}
		if strings.Contains(history[0].ID, base64.RawURLEncoding.EncodeToString([]byte(f.request.ActionID))) {
			t.Fatal("buyer-visible message ID exposed the internal action ID")
		}
		reply, err := f.store.SubmitBuyerReply(t.Context(), f.request.Thread, contracts.BuyerReplyInput{
			ExternalReplyID: "reply", Text: "I accept", ReplyToMessageID: result.Receipt.MessageID,
		})
		if err != nil || reply.Event.Message.LeadID != f.lead.ID || reply.Event.Message.SellerID != f.lead.SellerID {
			t.Fatalf("reply routing = %#v, %v", reply, err)
		}
	})

	t.Run("live checkout can hold the last unit", func(t *testing.T) {
		f := newSendFixture(t)
		f.reserve()
		result, err := f.sender.SendAgentMessage(t.Context(), f.request)
		if err != nil || result.Status != contracts.AgentDeliveryDelivered {
			t.Fatalf("send = %#v, %v", result, err)
		}
		history := f.history(t)
		if len(history) != 1 || history[0].Text != "Checkout for Rp 5400000: "+checkoutURL {
			t.Fatalf("history = %#v", history)
		}
		encoded, err := json.Marshal(result)
		if err != nil || strings.Contains(string(encoded), "stored-opaque_token") {
			t.Fatalf("unexpected receipt projection: %s, %v", encoded, err)
		}
		page, err := f.store.ReadBuyerReplies(t.Context(), "", 0)
		if err != nil || len(page.Events) != 0 {
			t.Fatalf("outbound send entered buyer feed: %#v, %v", page, err)
		}
	})

	for _, test := range []struct {
		name   string
		change func(*sendFixture)
		code   contracts.Code
	}{
		{"wrong seller", func(f *sendFixture) { f.request.SellerID = "other" }, contracts.InvalidArgument},
		{"wrong thread", func(f *sendFixture) { f.request.Thread.ThreadID = "other" }, contracts.InvalidArgument},
		{"unknown lead", func(f *sendFixture) { f.request.LeadID = "other" }, contracts.NotFound},
		{"wrong buyer", func(f *sendFixture) { f.lead.BuyerID = "other" }, contracts.InvalidArgument},
		{"wrong post", func(f *sendFixture) { f.lead.PostID = "other" }, contracts.InvalidArgument},
		{"wrong offer lead", func(f *sendFixture) { f.lead.Offer.LeadID = "other" }, contracts.InvalidState},
		{"missing offer", func(f *sendFixture) { f.lead.Offer = nil }, contracts.InvalidState},
		{"below item floor", func(f *sendFixture) { f.lead.Offer.PriceIDR-- }, contracts.PriceBelowMinimum},
		{"below seller discount minimum", func(f *sendFixture) {
			f.rules.FloorPriceIDR, f.rules.MaxDiscountBPS = 1, 0
			f.item.ListPriceIDR = f.lead.Offer.PriceIDR + 1
		}, contracts.PriceBelowMinimum},
		{"out of stock", func(f *sendFixture) { f.item.AvailableStock = 0 }, contracts.OutOfStock},
		{"unknown placeholder", func(f *sendFixture) { f.request.Text = "I can offer this item for {amount}." }, contracts.InvalidArgument},
		{"conflicting price", func(f *sendFixture) { f.request.Text = "Pay 1 rupiah instead of {price}." }, contracts.InvalidArgument},
		{"payment URL in model text", func(f *sendFixture) { f.request.Text = "Checkout for {price}: " + checkoutURL }, contracts.InvalidArgument},
		{"arbitrary payment instructions", func(f *sendFixture) { f.request.Text = "Transfer to account 1234." }, contracts.InvalidArgument},
		{"escalated", func(f *sendFixture) { f.lead.State = domain.LeadEscalated }, contracts.InvalidState},
		{"lost", func(f *sendFixture) { f.lead.State = domain.LeadLost }, contracts.InvalidState},
		{"paid", func(f *sendFixture) { f.lead.State = domain.LeadPaid }, contracts.InvalidState},
		{"reserved negotiation", func(f *sendFixture) { f.reserve(); f.request.Text = marketplace.AgentOfferText }, contracts.InvalidState},
		{"missing same-turn checkout", func(f *sendFixture) { f.reserve(); f.request.CheckoutID = "" }, contracts.InvalidState},
		{"wrong same-turn checkout", func(f *sendFixture) { f.reserve(); f.request.CheckoutID = "old" }, contracts.InvalidState},
		{"wrong checkout lead", func(f *sendFixture) { f.reserve(); f.lead.Checkout.LeadID = "other" }, contracts.InvalidState},
		{"wrong checkout offer", func(f *sendFixture) { f.reserve(); f.lead.Checkout.OfferID = "other" }, contracts.InvalidState},
		{"wrong checkout price", func(f *sendFixture) { f.reserve(); f.lead.Checkout.PriceIDR-- }, contracts.InvalidState},
		{"expired checkout", func(f *sendFixture) { f.reserve(); f.lead.Checkout.ExpiresAt = f.now }, contracts.CheckoutExpired},
		{"cancelled checkout", func(f *sendFixture) { f.reserve(); f.lead.Checkout.Status = domain.CheckoutCancelled }, contracts.InvalidState},
		{"direct message", func(f *sendFixture) { f.request.Channel = contracts.AgentDirectMessage }, contracts.AgentChannelUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSendFixture(t)
			test.change(f)
			result, err := f.sender.SendAgentMessage(t.Context(), f.request)
			assertSendCode(t, err, test.code)
			if result.Status == contracts.AgentDeliveryDelivered || len(f.history(t)) != 0 {
				t.Fatalf("rejected send affected history: %#v", result)
			}
		})
	}

	for _, rawURL := range []string{
		"https://external.example/checkout/token", "https://checkout.example.evil/checkout/token",
		"https://checkout.example@external.example/checkout/token", "https://name@checkout.example/checkout/token",
		"http://checkout.example/checkout/token", "//checkout.example/checkout/token", "https://checkout.example/checkout/",
		"https://checkout.example/pay/token", "https://checkout.example/checkout/token?next=https://external.example",
		"https://checkout.example/checkout/token#fragment", "https://checkout.example/checkout/%74oken",
		"https://checkout.example/checkout/token/other", "https://checkout.example/checkout/..",
	} {
		t.Run(rawURL, func(t *testing.T) {
			f := newSendFixture(t)
			f.reserve()
			f.lead.Checkout.URL = rawURL
			_, err := f.sender.SendAgentMessage(t.Context(), f.request)
			assertSendCode(t, err, contracts.InvalidState)
			if len(f.history(t)) != 0 {
				t.Fatal("unsafe checkout link reached the buyer")
			}
		})
	}
}

func TestCheckoutOriginRequiresASCIIHostname(t *testing.T) {
	f := newSendFixture(t)
	for _, origin := range []string{
		"https://%CF%83.example", "https://%CF%82.example", "https://σ.example",
		"https://-checkout.example", "https://checkout..example", "https://[checkout.example]",
		"https://checkout.example:", "https://checkout.example:65536", "https://[fe80::1%25lo0]",
	} {
		t.Run(origin, func(t *testing.T) {
			_, err := marketplace.NewAgentSender(f.commerce, f.store, f.store, origin, nil)
			assertSendCode(t, err, contracts.InvalidArgument)
		})
	}
	for _, origin := range []string{"https://xn--4xa.example", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		t.Run(origin, func(t *testing.T) {
			f := newSendFixture(t)
			f.reserve()
			f.lead.Checkout.URL = origin + "/checkout/token"
			sender, err := marketplace.NewAgentSender(f.commerce, f.store, f.store, origin, func() time.Time { return f.now })
			if err != nil {
				t.Fatal(err)
			}
			result, err := sender.SendAgentMessage(t.Context(), f.request)
			if err != nil || result.Status != contracts.AgentDeliveryDelivered || len(f.history(t)) != 1 {
				t.Fatalf("send = %#v, %v", result, err)
			}
		})
	}
}

func TestDeliveryReplay(t *testing.T) {
	t.Run("pending survives failure before publication and revalidates", func(t *testing.T) {
		f := newSendFixture(t)
		f.reserve()
		failure := errors.New("publisher unavailable for " + checkoutURL)
		publisher := faultPublisher{AgentMessagePublisher: f.store, send: func(ctx context.Context, publication contracts.AgentPublication) (contracts.AgentDeliveryReceipt, error) {
			draft, found, err := f.store.FindAgentDraft(ctx, publication.Draft.Request.ActionID)
			if err != nil || !found || draft.Request != f.request {
				t.Fatalf("publish before saving pending draft: %#v, %v", draft.Request, err)
			}
			return contracts.AgentDeliveryReceipt{}, failure
		}}
		sender := f.withPublisher(t, publisher)
		pending, err := sender.SendAgentMessage(t.Context(), f.request)
		if !errors.Is(err, failure) || strings.Contains(err.Error(), checkoutURL) || pending.Status != contracts.AgentDeliveryPending || len(f.history(t)) != 0 {
			t.Fatalf("send = %#v, %v", pending, err)
		}
		f.now = f.lead.Checkout.ExpiresAt
		_, err = f.sender.SendAgentMessage(t.Context(), f.request)
		assertSendCode(t, err, contracts.CheckoutExpired)
		if len(f.history(t)) != 0 {
			t.Fatal("retry published an expired link")
		}
	})

	t.Run("retry before expiry delivers one pending action", func(t *testing.T) {
		f := newSendFixture(t)
		f.reserve()
		failing := faultPublisher{AgentMessagePublisher: f.store, send: func(context.Context, contracts.AgentPublication) (contracts.AgentDeliveryReceipt, error) {
			return contracts.AgentDeliveryReceipt{}, errors.New("unavailable")
		}}
		if result, err := f.withPublisher(t, failing).SendAgentMessage(t.Context(), f.request); err == nil || result.Status != contracts.AgentDeliveryPending {
			t.Fatalf("initial send = %#v, %v", result, err)
		}
		conflict := f.request
		conflict.Text = marketplace.AgentOfferText
		_, err := f.sender.SendAgentMessage(t.Context(), conflict)
		assertSendCode(t, err, contracts.IdempotencyConflict)
		result, err := f.sender.SendAgentMessage(t.Context(), f.request)
		if err != nil || result.Status != contracts.AgentDeliveryDelivered || len(f.history(t)) != 1 {
			t.Fatalf("retry = %#v, %v", result, err)
		}
	})

	t.Run("changed stored offer cannot replace pending terms", func(t *testing.T) {
		f := newSendFixture(t)
		failing := faultPublisher{AgentMessagePublisher: f.store, send: func(context.Context, contracts.AgentPublication) (contracts.AgentDeliveryReceipt, error) {
			return contracts.AgentDeliveryReceipt{}, errors.New("unavailable")
		}}
		if result, err := f.withPublisher(t, failing).SendAgentMessage(t.Context(), f.request); err == nil || result.Status != contracts.AgentDeliveryPending {
			t.Fatalf("initial send = %#v, %v", result, err)
		}
		f.lead.Offer.PriceIDR++
		_, err := f.sender.SendAgentMessage(t.Context(), f.request)
		assertSendCode(t, err, contracts.InvalidState)
		if len(f.history(t)) != 0 {
			t.Fatal("retry published different terms under the original action")
		}
	})

	t.Run("receipt survives lost acknowledgment and expired checkout", func(t *testing.T) {
		f := newSendFixture(t)
		f.reserve()
		failure := errors.New("acknowledgment lost")
		var original contracts.AgentDeliveryReceipt
		publisher := faultPublisher{AgentMessagePublisher: f.store, send: func(ctx context.Context, publication contracts.AgentPublication) (contracts.AgentDeliveryReceipt, error) {
			var err error
			original, err = f.store.PublishAgentMessage(ctx, publication)
			if err != nil {
				t.Fatal(err)
			}
			return contracts.AgentDeliveryReceipt{}, failure
		}}
		first, err := f.withPublisher(t, publisher).SendAgentMessage(t.Context(), f.request)
		if !errors.Is(err, failure) || first.Status != contracts.AgentDeliveryPending || len(f.history(t)) != 1 {
			t.Fatalf("send = %#v, %v", first, err)
		}
		f.now = f.lead.Checkout.ExpiresAt
		f.commerce.GetLeadFunc = func(context.Context, string) (domain.LeadRecord, error) {
			t.Fatal("a delivered replay read current commerce facts")
			return domain.LeadRecord{}, nil
		}
		result, err := f.sender.SendAgentMessage(t.Context(), f.request)
		if err != nil || result.Status != contracts.AgentDeliveryDelivered || result.Receipt != original || len(f.history(t)) != 1 {
			t.Fatalf("replay = %#v, %v", result, err)
		}
		conflict := f.request
		conflict.SellerID = "other"
		_, err = f.sender.SendAgentMessage(t.Context(), conflict)
		assertSendCode(t, err, contracts.IdempotencyConflict)
	})

	t.Run("checkout expires between validation and publication", func(t *testing.T) {
		f := newSendFixture(t)
		f.reserve()
		publisher := faultPublisher{AgentMessagePublisher: f.store, send: func(ctx context.Context, publication contracts.AgentPublication) (contracts.AgentDeliveryReceipt, error) {
			f.now = f.lead.Checkout.ExpiresAt
			return f.store.PublishAgentMessage(ctx, publication)
		}}
		_, err := f.withPublisher(t, publisher).SendAgentMessage(t.Context(), f.request)
		assertSendCode(t, err, contracts.CheckoutExpired)
		if len(f.history(t)) != 0 {
			t.Fatal("publication did not check its deadline")
		}
	})

	t.Run("concurrent retry has one receipt and one visible message", func(t *testing.T) {
		f := newSendFixture(t)
		const attempts = 24
		results := make(chan contracts.AgentDeliveryReceipt, attempts)
		var wg sync.WaitGroup
		for range attempts {
			wg.Go(func() {
				result, err := f.sender.SendAgentMessage(t.Context(), f.request)
				if err != nil || result.Status != contracts.AgentDeliveryDelivered {
					t.Errorf("send = %#v, %v", result, err)
					return
				}
				results <- result.Receipt
			})
		}
		wg.Wait()
		close(results)
		history := f.history(t)
		if len(history) != 1 {
			t.Fatalf("history = %#v", history)
		}
		for receipt := range results {
			if receipt.MessageID != history[0].ID || receipt.PublishedAt != history[0].CreatedAt {
				t.Fatalf("receipt = %#v, history = %#v", receipt, history)
			}
		}
	})
}

func TestDeliveryReplayReconcilesConcurrentPublication(t *testing.T) {
	for _, variant := range []string{"prepare rejection", "stale stage", "conflicting request", "canceled caller"} {
		t.Run(variant, func(t *testing.T) {
			f := newSendFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			firstCtx, cancelFirst := context.WithCancel(ctx)
			defer cancelFirst()
			entered, release := make(chan struct{}), make(chan struct{})
			blocked := func() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			var leadReads atomic.Int32
			firstLead := f.lead
			outbox := contracts.AgentOutbox(f.store)
			if variant == "stale stage" {
				differentOffer := *firstLead.Offer
				differentOffer.PriceIDR++
				firstLead.Offer = &differentOffer
				var saves atomic.Int32
				outbox = blockingOutbox{AgentOutbox: f.store, beforeSave: func() {
					if saves.Add(1) == 1 {
						blocked()
					}
				}}
			} else {
				firstLead.State = domain.LeadLost
			}
			f.commerce.GetLeadFunc = func(context.Context, string) (domain.LeadRecord, error) {
				if leadReads.Add(1) == 1 {
					if variant != "stale stage" {
						blocked()
					}
					return firstLead, nil
				}
				return f.lead, nil
			}
			sender, err := marketplace.NewAgentSender(f.commerce, outbox, f.store, "https://checkout.example", func() time.Time { return f.now })
			if err != nil {
				t.Fatal(err)
			}
			firstRequest := f.request
			if variant == "conflicting request" {
				firstRequest.Text = marketplace.AgentAcknowledgmentText
			}
			var first contracts.AgentDeliveryResult
			var firstErr error
			done := make(chan struct{})
			go func() {
				defer close(done)
				first, firstErr = sender.SendAgentMessage(firstCtx, firstRequest)
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first send did not reach the race point")
			}
			published, err := sender.SendAgentMessage(ctx, f.request)
			if variant == "canceled caller" {
				cancelFirst()
			}
			close(release)
			<-done
			if err != nil || published.Status != contracts.AgentDeliveryDelivered || len(f.history(t)) != 1 {
				t.Fatalf("concurrent send = %#v, %v", published, err)
			}
			switch variant {
			case "conflicting request":
				assertSendCode(t, firstErr, contracts.IdempotencyConflict)
			case "canceled caller":
				if !errors.Is(firstErr, context.Canceled) {
					t.Fatalf("canceled caller got %#v, %v", first, firstErr)
				}
			default:
				if firstErr != nil || first != published {
					t.Fatalf("identical send = %#v, %v, want %#v", first, firstErr, published)
				}
			}
		})
	}
}

type blockingOutbox struct {
	contracts.AgentOutbox
	beforeSave func()
}

func (s blockingOutbox) SaveAgentDraft(ctx context.Context, draft contracts.AgentDeliveryDraft) error {
	s.beforeSave()
	return s.AgentOutbox.SaveAgentDraft(ctx, draft)
}

func TestDeliveryLimits(t *testing.T) {
	t.Run("lead cap remains atomic across distinct actions", func(t *testing.T) {
		f := newSendFixture(t)
		f.rules.MessageCap = 1
		var wg sync.WaitGroup
		for n := range 12 {
			wg.Go(func() {
				request := f.request
				request.ActionID = fmt.Sprintf("action-%d", n)
				request.Text = marketplace.AgentAcknowledgmentText
				result, err := f.sender.SendAgentMessage(t.Context(), request)
				if err == nil {
					if result.Status != contracts.AgentDeliveryDelivered {
						t.Errorf("unexpected result: %#v", result)
					}
					return
				}
				assertSendCode(t, err, contracts.AgentMessageCapReached)
			})
		}
		wg.Wait()
		if len(f.history(t)) != 1 {
			t.Fatal("lead cap allowed more than one message")
		}
	})

	t.Run("seller and platform rate limit uses a rolling minute", func(t *testing.T) {
		f := newSendFixture(t)
		f.rules.RateLimitPerMinute = 1
		if _, err := f.sender.SendAgentMessage(t.Context(), f.request); err != nil {
			t.Fatal(err)
		}
		second := f.request
		second.ActionID = "another"
		second.Text = marketplace.AgentAcknowledgmentText
		f.now = f.now.Add(59 * time.Second)
		_, err := f.sender.SendAgentMessage(t.Context(), second)
		assertSendCode(t, err, contracts.AgentRateLimited)
		f.now = f.now.Add(time.Second)
		if result, err := f.sender.SendAgentMessage(t.Context(), second); err != nil || result.Status != contracts.AgentDeliveryDelivered {
			t.Fatalf("after rolling window = %#v, %v", result, err)
		}
	})

	t.Run("one offer message per stored offer", func(t *testing.T) {
		f := newSendFixture(t)
		if _, err := f.sender.SendAgentMessage(t.Context(), f.request); err != nil {
			t.Fatal(err)
		}
		second := f.request
		second.ActionID = "another"
		_, err := f.sender.SendAgentMessage(t.Context(), second)
		assertSendCode(t, err, contracts.InvalidState)
		if len(f.history(t)) != 1 {
			t.Fatal("same stored offer published twice")
		}
	})
}

type sendFixture struct {
	now      time.Time
	lead     domain.LeadRecord
	item     domain.Item
	rules    domain.Rules
	request  contracts.AgentMessageRequest
	commerce *commercefake.Fake
	store    *mock.Threads
	sender   *marketplace.AgentSender
}

func newSendFixture(t *testing.T) *sendFixture {
	t.Helper()
	f := &sendFixture{now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), commerce: commercefake.NewSeeded()}
	var err error
	f.lead, err = f.commerce.GetLead(t.Context(), commercefake.LeadID)
	if err != nil {
		t.Fatal(err)
	}
	f.item, err = f.commerce.GetItem(t.Context(), f.lead.SellerID, f.lead.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	f.rules, err = f.commerce.GetRules(t.Context(), f.lead.SellerID, f.lead.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	f.lead.Offer = &domain.Offer{ID: "offer", LeadID: f.lead.ID, ItemID: f.lead.ItemID, Qty: 1, PriceIDR: 5_400_000}
	f.request = contracts.AgentMessageRequest{
		ActionID: "initial-action-with-internal-runtime-metadata", LeadID: f.lead.ID, SellerID: f.lead.SellerID,
		Thread:  domain.ThreadRef{Platform: f.lead.Platform, ThreadID: f.lead.ThreadID},
		Channel: contracts.AgentPublicReply, Text: marketplace.AgentOfferText,
	}
	f.commerce.GetLeadFunc = func(_ context.Context, id string) (domain.LeadRecord, error) {
		if id != f.lead.ID {
			return domain.LeadRecord{}, &contracts.Error{Code: contracts.NotFound, Message: "Lead not found"}
		}
		return f.lead, nil
	}
	f.commerce.GetItemFunc = func(context.Context, string, string) (domain.Item, error) { return f.item, nil }
	f.commerce.GetRulesFunc = func(context.Context, string, string) (domain.Rules, error) { return f.rules, nil }
	post := domain.Post{PostRef: domain.PostRef{Platform: f.lead.Platform, ExternalPostID: f.lead.PostID}, BuyerID: f.lead.BuyerID, ThreadID: f.lead.ThreadID}
	post.Text, post.CreatedAt = "Looking for a used laptop", f.now
	fixture, err := marketplacetest.NewConversation(t.Context(), post, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.store = fixture.Threads
	f.sender = f.withPublisher(t, f.store)
	return f
}

func (f *sendFixture) withPublisher(t *testing.T, publisher contracts.AgentMessagePublisher) *marketplace.AgentSender {
	t.Helper()
	sender, err := marketplace.NewAgentSender(f.commerce, f.store, publisher, "https://checkout.example", func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	return sender
}

func (f *sendFixture) reserve() {
	f.lead.State = domain.LeadReserved
	f.item.AvailableStock = 0
	f.lead.Checkout = &domain.Checkout{
		ID: "checkout", LeadID: f.lead.ID, ItemID: f.lead.ItemID, OfferID: f.lead.Offer.ID, Qty: 1,
		PriceIDR: f.lead.Offer.PriceIDR, TotalIDR: f.lead.Offer.PriceIDR,
		Status: domain.CheckoutPending, URL: checkoutURL, ExpiresAt: f.now.Add(30 * time.Minute),
	}
	f.request.CheckoutID = f.lead.Checkout.ID
	f.request.Text = marketplace.AgentCheckoutText
}

func (f *sendFixture) history(t *testing.T) []domain.Message {
	t.Helper()
	ref := domain.ThreadRef{Platform: "mock", ThreadID: "thread_demo"}
	history, err := f.store.ThreadHistory(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return history
}

func assertSendCode(t *testing.T, err error, code contracts.Code) {
	t.Helper()
	var serviceErr *contracts.Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != code {
		t.Errorf("error = %v, want %s", err, code)
	}
}

type sendPostLookup struct{ post domain.Post }

func (s sendPostLookup) GetPost(context.Context, domain.PostRef) (domain.Post, error) {
	return s.post, nil
}

type faultPublisher struct {
	contracts.AgentMessagePublisher
	send func(context.Context, contracts.AgentPublication) (contracts.AgentDeliveryReceipt, error)
}

func (f faultPublisher) PublishAgentMessage(ctx context.Context, publication contracts.AgentPublication) (contracts.AgentDeliveryReceipt, error) {
	return f.send(ctx, publication)
}
