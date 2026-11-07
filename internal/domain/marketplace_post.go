package domain

import "time"

// PostRef is the canonical identity shared by lookup and discovery.
type PostRef struct {
	Platform       string
	ExternalPostID string
}

// Post is an original marketplace record. A zero Location means unknown.
type Post struct {
	PostRef
	BuyerID   string
	ThreadID  string
	Text      string
	CreatedAt time.Time
	Location  Location
}
