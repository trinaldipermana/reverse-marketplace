package contracts

import (
	"context"

	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// PostLookup is the proposed canonical post reader for P1 and P2.
type PostLookup interface {
	GetPost(ctx context.Context, ref domain.PostRef) (domain.Post, error)
}

// MarketplaceDiscovery is the proposed P4 discovery boundary for P2 (issue #7).
// Its read operations leave lead creation and agent turns to the consumers.
type MarketplaceDiscovery interface {
	PostLookup
	SearchPost(ctx context.Context, req SearchPostRequest) (SearchPostResult, error)
}

type SearchPostRequest struct {
	Query  string
	City   string
	Cursor string
	Limit  int
}

type SearchPostResult struct {
	Posts      []domain.Post
	NextCursor string // Empty when the search is exhausted.
}

// PostFeed reads new posts in the connector's forward ingestion order.
type PostFeed interface {
	ReadPosts(ctx context.Context, cursor string, limit int) (ReadPostsResult, error)
}

type ReadPostsResult struct {
	Posts      []domain.Post
	NextCursor string // Checkpoint through this page, including an empty page.
}
