package mock

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// Threads is a local, in-memory thread history and buyer-reply feed.
type Threads struct {
	lookup contracts.PostLookup
	now    func() time.Time

	mu       sync.RWMutex
	relayMu  sync.Mutex
	threads  map[domain.ThreadRef]*thread
	replies  []storedBuyerReply
	replyIDs map[string]int
	cursorID string
}

type thread struct {
	post     domain.PostRef
	buyerID  string
	messages []domain.Message
}

type storedBuyerReply struct {
	event   contracts.BuyerReplyEvent
	pending bool
}

var (
	_ contracts.ThreadStore    = (*Threads)(nil)
	_ contracts.BuyerReplyFeed = (*Threads)(nil)
)

// NewThreads creates a store for canonical posts from lookup. A nil clock uses
// the system clock.
func NewThreads(lookup contracts.PostLookup, now func() time.Time) *Threads {
	if now == nil {
		now = time.Now
	}
	return &Threads{
		lookup:   lookup,
		now:      now,
		threads:  make(map[domain.ThreadRef]*thread),
		replyIDs: make(map[string]int),
		cursorID: rand.Text(),
	}
}

// OpenThread binds a platform-qualified thread to one immutable post identity.
// Reopening the same binding is safe.
func (s *Threads) OpenThread(ctx context.Context, ref domain.PostRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validPostRef(ref) {
		return invalidReplyArgument("Platform and external post ID are required")
	}
	if s.lookup == nil {
		return &contracts.Error{Code: contracts.DependencyUnavailable, Message: "Post lookup unavailable"}
	}
	post, err := s.lookup.GetPost(ctx, ref)
	if err != nil {
		return err
	}
	if post.PostRef != ref || !validMarketplaceIdentifier(post.BuyerID) || !validMarketplaceIdentifier(post.ThreadID) {
		return &contracts.Error{Code: contracts.DependencyUnavailable, Message: "Post lookup returned invalid thread identity"}
	}
	key := domain.ThreadRef{Platform: ref.Platform, ThreadID: post.ThreadID}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if existing, found := s.threads[key]; found {
		if existing.post != ref || existing.buyerID != post.BuyerID {
			return &contracts.Error{Code: contracts.IdempotencyConflict, Message: "Thread is bound to another post or buyer"}
		}
		return nil
	}
	s.threads[key] = &thread{post: ref, buyerID: post.BuyerID}
	return nil
}

// SubmitBuyerReply records the message and its pending notification together.
func (s *Threads) SubmitBuyerReply(ctx context.Context, ref domain.ThreadRef, input contracts.BuyerReplyInput) (contracts.BuyerReplyResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.BuyerReplyResult{}, err
	}
	if err := validateBuyerReply(ref, input); err != nil {
		return contracts.BuyerReplyResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contracts.BuyerReplyResult{}, err
	}
	thread, found := s.threads[ref]
	if !found {
		return contracts.BuyerReplyResult{}, &contracts.Error{Code: contracts.NotFound, Message: "Thread not found"}
	}
	id := "buyer-reply:" + base64.RawURLEncoding.EncodeToString([]byte(ref.Platform)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(input.ExternalReplyID))
	if index, found := s.replyIDs[id]; found {
		event := s.replies[index].event
		message := event.Message
		if message.ThreadID != ref.ThreadID || message.Text != input.Text ||
			message.ReplyToMessageID != input.ReplyToMessageID || !reflect.DeepEqual(message.Location, input.Location) {
			return contracts.BuyerReplyResult{}, &contracts.Error{Code: contracts.IdempotencyConflict, Message: "External reply ID already used for another request"}
		}
		return contracts.BuyerReplyResult{Event: copyBuyerReply(event), Replayed: true}, nil
	}
	message := domain.Message{
		ID: id, PostRef: thread.post, ExternalReplyID: input.ExternalReplyID,
		BuyerID: thread.buyerID, ThreadID: ref.ThreadID, Direction: domain.MessageBuyer,
		Text: input.Text, CreatedAt: s.now().UTC(), Location: copyMessageLocation(input.Location),
		ReplyToMessageID: input.ReplyToMessageID,
	}
	for _, previous := range thread.messages {
		if previous.ID == input.ReplyToMessageID && previous.Direction == domain.MessageAgent {
			message.LeadID, message.SellerID = previous.LeadID, previous.SellerID
			break
		}
	}
	event := contracts.BuyerReplyEvent{ID: id, Message: message}
	thread.messages = append(thread.messages, message)
	s.replyIDs[id] = len(s.replies)
	s.replies = append(s.replies, storedBuyerReply{event: event, pending: true})
	return contracts.BuyerReplyResult{Event: copyBuyerReply(event)}, nil
}

func (s *Threads) ThreadHistory(ctx context.Context, ref domain.ThreadRef) ([]domain.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validThreadRef(ref) {
		return nil, invalidReplyArgument("Platform and thread ID are required")
	}
	s.mu.RLock()
	if err := ctx.Err(); err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	thread, found := s.threads[ref]
	if !found {
		s.mu.RUnlock()
		return nil, &contracts.Error{Code: contracts.NotFound, Message: "Thread not found"}
	}
	messages := make([]domain.Message, len(thread.messages))
	for index, message := range thread.messages {
		messages[index] = copyThreadMessage(message)
	}
	s.mu.RUnlock()
	slices.SortFunc(messages, func(a, b domain.Message) int {
		if order := a.CreatedAt.Compare(b.CreatedAt); order != 0 {
			return order
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return messages, nil
}

func (s *Threads) ReadBuyerReplies(ctx context.Context, cursor string, limit int) (contracts.BuyerReplyPage, error) {
	if err := ctx.Err(); err != nil {
		return contracts.BuyerReplyPage{}, err
	}
	if limit < 0 || limit > 100 {
		return contracts.BuyerReplyPage{}, invalidReplyArgument("Limit must be between 1 and 100, or zero for the default")
	}
	if limit == 0 {
		limit = 20
	}
	prefix := s.cursorID + ".replies."
	offset := 0
	if cursor != "" {
		position, ok := strings.CutPrefix(cursor, prefix)
		var err error
		offset, err = strconv.Atoi(position)
		if !ok || err != nil || offset < 0 {
			return contracts.BuyerReplyPage{}, invalidReplyArgument("Invalid buyer reply cursor")
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return contracts.BuyerReplyPage{}, err
	}
	if offset > len(s.replies) {
		return contracts.BuyerReplyPage{}, invalidReplyArgument("Invalid buyer reply cursor")
	}
	count := min(limit, len(s.replies)-offset)
	events := make([]contracts.BuyerReplyEvent, count)
	for i, reply := range s.replies[offset : offset+count] {
		events[i] = copyBuyerReply(reply.event)
	}
	return contracts.BuyerReplyPage{Events: events, NextCursor: prefix + strconv.Itoa(offset+count)}, nil
}

// RelayBuyerReplies attempts each currently pending reply once per pass.
// Concurrent passes coalesce. A volatile acknowledgment leaves a reply pending.
func (s *Threads) RelayBuyerReplies(ctx context.Context, intake contracts.BuyerReplyIntake) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if intake == nil {
		return &contracts.Error{Code: contracts.DependencyUnavailable, Message: "Buyer reply intake unavailable", Retryable: true}
	}
	if !s.relayMu.TryLock() {
		return nil
	}
	defer s.relayMu.Unlock()

	s.mu.RLock()
	pending := make([]contracts.BuyerReplyEvent, 0)
	for _, reply := range s.replies {
		if reply.pending {
			pending = append(pending, copyBuyerReply(reply.event))
		}
	}
	s.mu.RUnlock()

	var failures []error
	for _, event := range pending {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		ack, err := intake.AcceptBuyerReply(ctx, event)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		switch ack {
		case contracts.BuyerReplyAcceptedDurable:
			s.mu.Lock()
			s.replies[s.replyIDs[event.ID]].pending = false
			s.mu.Unlock()
		case contracts.BuyerReplyAcceptedVolatile:
			// Retry on a later pass with the same event ID.
		default:
			failures = append(failures, &contracts.Error{
				Code: contracts.DependencyUnavailable, Message: "Buyer reply intake returned an invalid acknowledgment", Retryable: true,
			})
		}
	}
	return errors.Join(failures...)
}

func validateBuyerReply(ref domain.ThreadRef, input contracts.BuyerReplyInput) error {
	if !validThreadRef(ref) || !validMarketplaceIdentifier(input.ExternalReplyID) {
		return invalidReplyArgument("Platform, thread ID, and external reply ID are required")
	}
	if input.ReplyToMessageID != "" && !validMarketplaceIdentifier(input.ReplyToMessageID) {
		return invalidReplyArgument("Invalid reply target ID")
	}
	if strings.TrimSpace(input.Text) == "" || !utf8.ValidString(input.Text) || !utf8.ValidString(input.Location.City) {
		return invalidReplyArgument("Reply text is required and text and city must be valid UTF-8")
	}
	if !validCoordinate(input.Location.Latitude, 90) || !validCoordinate(input.Location.Longitude, 180) {
		return invalidReplyArgument("Reply coordinates are out of range")
	}
	return nil
}

func validThreadRef(ref domain.ThreadRef) bool {
	return validMarketplaceIdentifier(ref.Platform) && validMarketplaceIdentifier(ref.ThreadID)
}

func copyBuyerReply(event contracts.BuyerReplyEvent) contracts.BuyerReplyEvent {
	event.Message = copyThreadMessage(event.Message)
	return event
}

func copyThreadMessage(message domain.Message) domain.Message {
	message.Location = copyMessageLocation(message.Location)
	return message
}

func copyMessageLocation(location domain.Location) domain.Location {
	if location.Latitude != nil {
		latitude := *location.Latitude
		location.Latitude = &latitude
	}
	if location.Longitude != nil {
		longitude := *location.Longitude
		location.Longitude = &longitude
	}
	return location
}

func invalidReplyArgument(message string) *contracts.Error {
	return &contracts.Error{Code: contracts.InvalidArgument, Message: message}
}
