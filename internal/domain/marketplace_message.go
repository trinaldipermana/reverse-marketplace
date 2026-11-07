package domain

import "time"

// ThreadRef qualifies a thread ID with the connector platform.
type ThreadRef struct {
	Platform string
	ThreadID string
}

type MessageDirection string

const (
	MessageBuyer MessageDirection = "buyer"
	MessageAgent MessageDirection = "agent"
)

// Message is a normalized marketplace message. Lead and seller IDs are
// internal routing hints. Location may be unknown.
type Message struct {
	ID string
	PostRef
	ExternalReplyID  string
	BuyerID          string
	ThreadID         string
	Direction        MessageDirection
	Text             string
	CreatedAt        time.Time
	Location         Location
	ReplyToMessageID string
	LeadID           string `json:"-"`
	SellerID         string `json:"-"`
}
