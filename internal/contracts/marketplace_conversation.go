package contracts

import (
	"context"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// ThreadStore is the proposed P4 mock HTTP boundary for issue #10.
type ThreadStore interface {
	SubmitBuyerReply(ctx context.Context, ref domain.ThreadRef, input BuyerReplyInput) (BuyerReplyResult, error)
	ThreadHistory(ctx context.Context, ref domain.ThreadRef) ([]domain.Message, error)
}

// BuyerReplyInput is buyer-supplied. The store resolves buyer and post identity
// from the thread and any routing hint from the referenced agent message.
type BuyerReplyInput struct {
	ExternalReplyID  string
	Text             string
	ReplyToMessageID string
	Location         domain.Location
}

type BuyerReplyResult struct {
	Event    BuyerReplyEvent
	Replayed bool
}

// BuyerReplyEvent has a stable ID for intake deduplication. P1 resolves lead
// eligibility before scheduling a turn. A routing hint can be absent.
type BuyerReplyEvent struct {
	ID      string
	Message domain.Message
}

type BuyerReplyAck string

const (
	BuyerReplyAcceptedVolatile BuyerReplyAck = "accepted_volatile"
	BuyerReplyAcceptedDurable  BuyerReplyAck = "accepted_durable"
)

// BuyerReplyIntake is the proposed P1 boundary. Durable acknowledgment means
// the event ID and its routing outcome or work to be processed survive a
// runtime restart. An in-memory enqueue must acknowledge as volatile.
type BuyerReplyIntake interface {
	AcceptBuyerReply(ctx context.Context, event BuyerReplyEvent) (BuyerReplyAck, error)
}

// BuyerReplyFeed reads saved buyer replies in insertion order, regardless of
// notification state. Consumers commit the cursor after durable ingestion.
type BuyerReplyFeed interface {
	ReadBuyerReplies(ctx context.Context, cursor string, limit int) (BuyerReplyPage, error)
}

type BuyerReplyPage struct {
	Events     []BuyerReplyEvent
	NextCursor string
}

// AgentDeliveryChannel describes a buyer-facing connector operation.
type AgentDeliveryChannel string

const (
	AgentPublicReply   AgentDeliveryChannel = "public_reply"
	AgentDirectMessage AgentDeliveryChannel = "direct_message"
)

// AgentMessageRequest is bound by trusted runtime code. Only Text comes from
// the model. CheckoutID identifies a checkout returned in this turn, if any.
// ActionID is stable across retries and globally unique within the outbox.
type AgentMessageRequest struct {
	ActionID   string
	LeadID     string
	SellerID   string
	Thread     domain.ThreadRef
	CheckoutID string
	Channel    AgentDeliveryChannel
	Text       string
}

type AgentDeliveryStatus string

const (
	AgentDeliveryPending   AgentDeliveryStatus = "pending"
	AgentDeliveryDelivered AgentDeliveryStatus = "delivered"
	AgentDeliveryRejected  AgentDeliveryStatus = "rejected"
)

const (
	AgentChannelUnsupported Code = "AGENT_CHANNEL_UNSUPPORTED"
	AgentMessageCapReached  Code = "AGENT_MESSAGE_CAP_REACHED"
	AgentRateLimited        Code = "AGENT_RATE_LIMITED"
)

// AgentDeliveryReceipt identifies a published message without exposing its
// text or the checkout bearer token.
type AgentDeliveryReceipt struct {
	ActionID    string
	MessageID   string
	PublishedAt time.Time
}

type AgentDeliveryResult struct {
	Status  AgentDeliveryStatus
	Receipt AgentDeliveryReceipt
}

// AgentDeliveryDraft binds one action to approved text and canonical routing.
// RenderedText can contain a checkout bearer token. Keep the draft internal.
type AgentDeliveryDraft struct {
	Request      AgentMessageRequest
	Post         domain.PostRef
	BuyerID      string
	OfferID      string
	RenderedText string `json:"-"`
	ExpiresAt    time.Time
}

// AgentOutbox retains the immutable draft through receipt reconciliation.
// SaveAgentDraft rejects conflicting requests or terms for an existing action.
type AgentOutbox interface {
	FindAgentDraft(ctx context.Context, actionID string) (AgentDeliveryDraft, bool, error)
	SaveAgentDraft(ctx context.Context, draft AgentDeliveryDraft) error
}

// AgentPublication carries current limits alongside the immutable draft.
type AgentPublication struct {
	Draft              AgentDeliveryDraft
	MessageCap         int
	RateLimitPerMinute int
}

// AgentMessagePublisher reconciles and idempotently publishes approved work.
// It checks the publication deadline and counts limits at the publish boundary.
// A receipt is evidence of publication and is recorded atomically with it in
// the mock connector. Repeating the same action returns the original receipt.
type AgentMessagePublisher interface {
	SupportsAgentChannel(channel AgentDeliveryChannel) bool
	FindAgentReceipt(ctx context.Context, actionID string) (AgentDeliveryReceipt, bool, error)
	PublishAgentMessage(ctx context.Context, publication AgentPublication) (AgentDeliveryReceipt, error)
}
