package mock

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func TestBuyerReplyRecovery(t *testing.T) {
	ctx := context.Background()
	store, thread := replyThread(t)
	input := contracts.BuyerReplyInput{ExternalReplyID: "reply-1", Text: "Is it available?"}
	result, err := store.SubmitBuyerReply(ctx, thread, input)
	if err != nil || result.Replayed || result.Event.ID == "" {
		t.Fatalf("submit = %+v, %v", result, err)
	}

	// Saving completes before delivery is attempted, so a failed relay keeps the
	// original reply visible and available for another notification attempt.
	deliveryFailure := errors.New("intake offline")
	failing := replyIntake(func(context.Context, contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error) {
		return "", deliveryFailure
	})
	if err := store.RelayBuyerReplies(ctx, failing); !errors.Is(err, deliveryFailure) {
		t.Fatalf("failed relay = %v, want %v", err, deliveryFailure)
	}
	history, err := store.ThreadHistory(ctx, thread)
	if err != nil || len(history) != 1 || history[0].ID != result.Event.Message.ID {
		t.Fatalf("history after failure = %+v, %v", history, err)
	}

	replay, err := store.SubmitBuyerReply(ctx, thread, input)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Event, result.Event) {
		t.Fatalf("replay = %+v, %v, want original %+v", replay, err, result.Event)
	}
	conflict := input
	conflict.Text = "Another request"
	_, err = store.SubmitBuyerReply(ctx, thread, conflict)
	assertReplyCode(t, err, contracts.IdempotencyConflict)

	volatileCalls := 0
	volatile := replyIntake(func(_ context.Context, event contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error) {
		volatileCalls++
		if !reflect.DeepEqual(event, result.Event) {
			t.Errorf("volatile event = %+v, want %+v", event, result.Event)
		}
		return contracts.BuyerReplyAcceptedVolatile, nil
	})
	for range 2 {
		if err := store.RelayBuyerReplies(ctx, volatile); err != nil {
			t.Fatal(err)
		}
	}
	if volatileCalls != 2 {
		t.Fatalf("volatile intake called %d times, want 2", volatileCalls)
	}

	// The intake can commit and then lose its response. It must see the same
	// stable event ID on retry so it can acknowledge its previous acceptance.
	durableEvents := map[string]struct{}{}
	durableCalls := 0
	responseLost := errors.New("lost intake acknowledgment")
	durable := replyIntake(func(_ context.Context, event contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error) {
		durableCalls++
		durableEvents[event.ID] = struct{}{}
		if durableCalls == 1 {
			return "", responseLost
		}
		return contracts.BuyerReplyAcceptedDurable, nil
	})
	if err := store.RelayBuyerReplies(ctx, durable); !errors.Is(err, responseLost) {
		t.Fatalf("relay after lost acknowledgment = %v", err)
	}
	for range 2 {
		if err := store.RelayBuyerReplies(ctx, durable); err != nil {
			t.Fatal(err)
		}
	}
	if durableCalls != 2 || len(durableEvents) != 1 {
		t.Fatalf("durable intake called %d times for events %v", durableCalls, durableEvents)
	}
	if replay, err := store.SubmitBuyerReply(ctx, thread, input); err != nil || !replay.Replayed {
		t.Fatalf("replay after durable acknowledgment = %+v, %v", replay, err)
	}
	if err := store.RelayBuyerReplies(ctx, durable); err != nil || durableCalls != 2 {
		t.Fatalf("acknowledged reply was reopened, calls=%d, err=%v", durableCalls, err)
	}
}

func TestBuyerReplyRecoveryConcurrent(t *testing.T) {
	ctx := context.Background()
	store, thread := replyThread(t)
	input := contracts.BuyerReplyInput{ExternalReplyID: "same-reply", Text: "Please confirm."}

	results := make(chan contracts.BuyerReplyResult, 20)
	var submissions sync.WaitGroup
	for range cap(results) {
		submissions.Go(func() {
			reply, err := store.SubmitBuyerReply(ctx, thread, input)
			if err != nil {
				t.Error(err)
			}
			results <- reply
		})
	}
	submissions.Wait()
	close(results)

	created := 0
	ids := map[string]struct{}{}
	for result := range results {
		if !result.Replayed {
			created++
		}
		ids[result.Event.ID] = struct{}{}
	}
	if created != 1 || len(ids) != 1 {
		t.Fatalf("created %d replies and %d event IDs", created, len(ids))
	}

	called, finish := make(chan struct{}, 1), make(chan struct{})
	intake := replyIntake(func(context.Context, contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error) {
		select {
		case called <- struct{}{}:
			<-finish
		default:
			t.Error("same pending event concurrently dispatched")
		}
		return contracts.BuyerReplyAcceptedDurable, nil
	})
	relayResult := make(chan error, 1)
	go func() { relayResult <- store.RelayBuyerReplies(ctx, intake) }()
	<-called
	called <- struct{}{}
	if err := store.RelayBuyerReplies(ctx, intake); err != nil {
		t.Error(err)
	}
	close(finish)
	if err := <-relayResult; err != nil {
		t.Error(err)
	}
}

func TestBuyerReplyRecoveryRoutingAndHistory(t *testing.T) {
	ctx := context.Background()
	store, thread := replyThread(t)
	post := domain.PostRef{Platform: thread.Platform, ExternalPostID: "post-1"}
	stamp := time.Date(2026, 11, 7, 4, 0, 0, 0, time.UTC)
	// PR3 owns publication. Model two already published messages at the same
	// time here to check the routing and history boundaries independently.
	store.threads[thread].messages = []domain.Message{
		{ID: "seller-b-msg", PostRef: post, ThreadID: thread.ThreadID, BuyerID: "buyer-1", Direction: domain.MessageAgent, LeadID: "lead-b", SellerID: "seller-b", CreatedAt: stamp},
		{ID: "seller-a-msg", PostRef: post, ThreadID: thread.ThreadID, BuyerID: "buyer-1", Direction: domain.MessageAgent, LeadID: "lead-a", SellerID: "seller-a", CreatedAt: stamp},
	}

	for i, target := range []string{"seller-b-msg", "", "not-in-thread"} {
		result, err := store.SubmitBuyerReply(ctx, thread, contracts.BuyerReplyInput{
			ExternalReplyID: fmt.Sprintf("reply-%d", i), Text: "Is this the final price?", ReplyToMessageID: target,
		})
		wantLead, wantSeller := "", ""
		if target == "seller-b-msg" {
			wantLead, wantSeller = "lead-b", "seller-b"
		}
		if err != nil || result.Event.Message.LeadID != wantLead || result.Event.Message.SellerID != wantSeller || result.Event.Message.ReplyToMessageID != target {
			t.Fatalf("reply target %q = %+v, %v", target, result, err)
		}
	}

	history, err := store.ThreadHistory(ctx, thread)
	if err != nil || len(history) != 5 || history[3].ID != "seller-a-msg" || history[4].ID != "seller-b-msg" {
		t.Fatalf("history = %+v, %v, want five messages and stable ID order at tied times", history, err)
	}

	var cursor string
	var replies []contracts.BuyerReplyEvent
	for range 3 {
		page, err := store.ReadBuyerReplies(ctx, cursor, 1)
		if err != nil || len(page.Events) != 1 || page.NextCursor == cursor || page.Events[0].Message.Direction != domain.MessageBuyer {
			t.Fatalf("page after %q = %+v, %v", cursor, page, err)
		}
		replies = append(replies, page.Events...)
		cursor = page.NextCursor
	}
	page, err := store.ReadBuyerReplies(ctx, cursor, 1)
	if err != nil || len(page.Events) != 0 || page.NextCursor != cursor {
		t.Fatalf("exhausted page = %+v, %v, want stable checkpoint", page, err)
	}
	for i, event := range replies {
		if want := fmt.Sprintf("reply-%d", i); event.Message.ExternalReplyID != want {
			t.Fatalf("event %d reply ID = %q, want %q", i, event.Message.ExternalReplyID, want)
		}
	}
	var relayed []contracts.BuyerReplyEvent
	if err := store.RelayBuyerReplies(ctx, replyIntake(func(_ context.Context, event contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error) {
		relayed = append(relayed, event)
		return contracts.BuyerReplyAcceptedDurable, nil
	})); err != nil || !reflect.DeepEqual(relayed, replies) {
		t.Fatalf("relay = %+v, %v, want only inbound events %+v", relayed, err, replies)
	}
}

func TestBuyerReplyRecoveryPlatformIsolation(t *testing.T) {
	ctx := context.Background()
	store, thread := replyThread(t)
	other := domain.ThreadRef{Platform: "other", ThreadID: thread.ThreadID}
	if err := store.OpenThread(ctx, domain.PostRef{Platform: other.Platform, ExternalPostID: "post-1"}); err != nil {
		t.Fatal(err)
	}
	input := contracts.BuyerReplyInput{ExternalReplyID: "reply-1", Text: "Still available?"}
	first, err := store.SubmitBuyerReply(ctx, thread, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.SubmitBuyerReply(ctx, other, input)
	if err != nil || second.Event.ID == first.Event.ID {
		t.Fatalf("same external identity on another platform = %+v, %v", second, err)
	}
	history, err := store.ThreadHistory(ctx, other)
	if err != nil || len(history) != 1 || history[0].BuyerID != "other-buyer" || history[0].Platform != other.Platform {
		t.Fatalf("other history = %+v, %v", history, err)
	}
	_, err = store.ReadBuyerReplies(ctx, "unknown-cursor", 1)
	assertReplyCode(t, err, contracts.InvalidArgument)
	_, err = store.ReadBuyerReplies(ctx, "", -1)
	assertReplyCode(t, err, contracts.InvalidArgument)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.SubmitBuyerReply(cancelled, thread, input)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled submit = %v", err)
	}
}

func TestBuyerReplyRecoveryCanonicalThread(t *testing.T) {
	ctx := context.Background()
	post := domain.Post{PostRef: domain.PostRef{Platform: "mock", ExternalPostID: "post-1"}, BuyerID: "buyer-1", ThreadID: "thread-1"}
	conflict := post
	conflict.ExternalPostID, conflict.BuyerID = "post-2", "buyer-2"
	store := NewThreads(replyPosts{post.PostRef: post, conflict.PostRef: conflict}, nil)
	if err := store.OpenThread(ctx, post.PostRef); err != nil {
		t.Fatal(err)
	}
	if err := store.OpenThread(ctx, post.PostRef); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	assertReplyCode(t, store.OpenThread(ctx, conflict.PostRef), contracts.IdempotencyConflict)
	assertReplyCode(t, store.OpenThread(ctx, domain.PostRef{Platform: "mock", ExternalPostID: "missing"}), contracts.NotFound)

	ref := domain.ThreadRef{Platform: post.Platform, ThreadID: post.ThreadID}
	result, err := store.SubmitBuyerReply(ctx, ref, contracts.BuyerReplyInput{ExternalReplyID: "reply", Text: "Hello"})
	if err != nil || result.Event.Message.PostRef != post.PostRef || result.Event.Message.BuyerID != post.BuyerID {
		t.Fatalf("reply after conflicting registration = %+v, %v", result, err)
	}
}

func TestBuyerReplyRecoveryLocationIsCopied(t *testing.T) {
	ctx := context.Background()
	store, thread := replyThread(t)
	latitude, longitude := -6.2, 106.8
	result, err := store.SubmitBuyerReply(ctx, thread, contracts.BuyerReplyInput{
		ExternalReplyID: "reply-1", Text: "Can we meet in Jakarta?",
		Location: domain.Location{City: "Jakarta", Latitude: &latitude, Longitude: &longitude},
	})
	if err != nil {
		t.Fatal(err)
	}
	latitude = 0
	*result.Event.Message.Location.Longitude = 0

	history, err := store.ThreadHistory(ctx, thread)
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	location := history[0].Location
	if *location.Latitude != -6.2 || *location.Longitude != 106.8 {
		t.Fatalf("mutated history location = %+v", location)
	}
	*location.Latitude = 0

	page, err := store.ReadBuyerReplies(ctx, "", 1)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("reply page = %+v, %v", page, err)
	}
	if got := *page.Events[0].Message.Location.Latitude; got != -6.2 {
		t.Fatalf("mutated reply feed latitude = %v", got)
	}
}

func replyThread(t *testing.T) (*Threads, domain.ThreadRef) {
	t.Helper()
	post := domain.Post{PostRef: domain.PostRef{Platform: "mock", ExternalPostID: "post-1"}, BuyerID: "buyer-1", ThreadID: "thread-1"}
	other := post
	other.Platform, other.BuyerID = "other", "other-buyer"
	store := NewThreads(replyPosts{post.PostRef: post, other.PostRef: other}, func() time.Time {
		return time.Date(2026, 11, 7, 4, 0, 0, 0, time.UTC)
	})
	if err := store.OpenThread(context.Background(), post.PostRef); err != nil {
		t.Fatal(err)
	}
	return store, domain.ThreadRef{Platform: post.Platform, ThreadID: post.ThreadID}
}

type replyPosts map[domain.PostRef]domain.Post

func (posts replyPosts) GetPost(_ context.Context, ref domain.PostRef) (domain.Post, error) {
	post, ok := posts[ref]
	if !ok {
		return domain.Post{}, &contracts.Error{Code: contracts.NotFound, Message: "post not found"}
	}
	return post, nil
}

type replyIntake func(context.Context, contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error)

func (intake replyIntake) AcceptBuyerReply(ctx context.Context, event contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error) {
	return intake(ctx, event)
}

func assertReplyCode(t *testing.T, err error, code contracts.Code) {
	t.Helper()
	var typed *contracts.Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}
