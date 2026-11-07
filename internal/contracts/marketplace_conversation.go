package contracts

import (
	"context"

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
