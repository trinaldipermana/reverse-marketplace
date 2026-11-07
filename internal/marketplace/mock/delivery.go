package mock

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

var (
	_ contracts.AgentOutbox           = (*Threads)(nil)
	_ contracts.AgentMessagePublisher = (*Threads)(nil)
)

func (s *Threads) SupportsAgentChannel(channel contracts.AgentDeliveryChannel) bool {
	return channel == contracts.AgentPublicReply
}

func (s *Threads) FindAgentDraft(ctx context.Context, actionID string) (contracts.AgentDeliveryDraft, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return contracts.AgentDeliveryDraft{}, false, err
	}
	draft, found := s.drafts[actionID]
	return draft, found, nil
}

func (s *Threads) SaveAgentDraft(ctx context.Context, draft contracts.AgentDeliveryDraft) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if stored, found := s.drafts[draft.Request.ActionID]; found {
		return matchAgentDraft(stored, draft)
	}
	if _, err := s.agentThread(draft); err != nil {
		return err
	}
	s.drafts[draft.Request.ActionID] = draft
	return nil
}

func (s *Threads) FindAgentReceipt(ctx context.Context, actionID string) (contracts.AgentDeliveryReceipt, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return contracts.AgentDeliveryReceipt{}, false, err
	}
	receipt, found := s.receipts[actionID]
	return receipt, found, nil
}

// PublishAgentMessage atomically exposes the message and saves its receipt.
// The same lock also protects the lead cap and seller/platform rate window.
func (s *Threads) PublishAgentMessage(ctx context.Context, publication contracts.AgentPublication) (contracts.AgentDeliveryReceipt, error) {
	var receipt contracts.AgentDeliveryReceipt
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	draft := publication.Draft
	req := draft.Request
	stored, found := s.drafts[req.ActionID]
	if !found {
		return receipt, &contracts.Error{Code: contracts.InvalidState, Message: "Pending message is required before publication"}
	}
	if err := matchAgentDraft(stored, draft); err != nil {
		return receipt, err
	}
	if receipt, delivered := s.receipts[req.ActionID]; delivered {
		return receipt, nil
	}
	thread, err := s.agentThread(draft)
	if err != nil {
		return receipt, err
	}
	now := s.now().UTC()
	if !draft.ExpiresAt.IsZero() && !draft.ExpiresAt.After(now) {
		return receipt, &contracts.Error{Code: contracts.CheckoutExpired, Message: "Checkout expired before publication"}
	}
	if err := s.checkAgentLimits(publication, now); err != nil {
		return receipt, err
	}
	message := domain.Message{
		ID: "agent-message:" + rand.Text(), PostRef: thread.post,
		BuyerID: thread.buyerID, ThreadID: req.Thread.ThreadID, Direction: domain.MessageAgent,
		Text: draft.RenderedText, CreatedAt: now, LeadID: req.LeadID, SellerID: req.SellerID,
	}
	receipt = contracts.AgentDeliveryReceipt{ActionID: req.ActionID, MessageID: message.ID, PublishedAt: message.CreatedAt}
	thread.messages = append(thread.messages, message)
	s.receipts[req.ActionID] = receipt
	return receipt, nil
}

func (s *Threads) agentThread(draft contracts.AgentDeliveryDraft) (*thread, error) {
	req := draft.Request
	if !s.SupportsAgentChannel(req.Channel) {
		return nil, &contracts.Error{Code: contracts.AgentChannelUnsupported, Message: "Agent delivery channel is unsupported"}
	}
	thread, found := s.threads[req.Thread]
	if !found {
		return nil, &contracts.Error{Code: contracts.NotFound, Message: "Thread not found"}
	}
	if thread.post != draft.Post || thread.buyerID != draft.BuyerID {
		return nil, &contracts.Error{Code: contracts.InvalidArgument, Message: "Delivery scope does not match the marketplace thread"}
	}
	return thread, nil
}

func (s *Threads) checkAgentLimits(publication contracts.AgentPublication, now time.Time) error {
	draft := publication.Draft
	req := draft.Request
	windowStart := now.Add(-time.Minute)
	leadCount, sellerCount := 0, 0
	for actionID, receipt := range s.receipts {
		previous := s.drafts[actionID]
		if previous.Request.LeadID == req.LeadID {
			leadCount++
			if req.CheckoutID != "" && req.CheckoutID == previous.Request.CheckoutID ||
				req.CheckoutID == "" && previous.Request.CheckoutID == "" && draft.OfferID != "" && draft.OfferID == previous.OfferID {
				return &contracts.Error{Code: contracts.InvalidState, Message: "Commercial terms have already been sent"}
			}
		}
		if previous.Request.SellerID == req.SellerID && previous.Request.Thread.Platform == req.Thread.Platform && receipt.PublishedAt.After(windowStart) {
			sellerCount++
		}
	}
	if leadCount >= publication.MessageCap {
		return &contracts.Error{Code: contracts.AgentMessageCapReached, Message: "Lead reached its outbound message cap"}
	}
	if sellerCount >= publication.RateLimitPerMinute {
		return &contracts.Error{Code: contracts.AgentRateLimited, Message: "Seller reached its marketplace send rate", Retryable: true}
	}
	return nil
}

func matchAgentDraft(stored, draft contracts.AgentDeliveryDraft) error {
	if stored.Request != draft.Request {
		return &contracts.Error{Code: contracts.IdempotencyConflict, Message: "Action ID is already bound to another request"}
	}
	if stored != draft {
		return &contracts.Error{Code: contracts.InvalidState, Message: "Pending message no longer matches current terms"}
	}
	return nil
}
