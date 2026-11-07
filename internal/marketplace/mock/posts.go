package mock

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

const (
	defaultPostPageSize = 20
	maxPostPageSize     = 100
)

// PostStore holds immutable posts, their thread bindings, replay keys, and
// feed order for one mock marketplace instance. Construct it with NewPostStore.
type PostStore struct {
	mu         sync.RWMutex
	posts      map[domain.PostRef]domain.Post
	threads    map[domain.ThreadRef]struct{}
	replayKeys map[string]domain.PostRef
	feed       []domain.PostRef
	cursorID   string
}

// NewPostStore validates and copies seed posts, in the given feed order.
func NewPostStore(seed ...domain.Post) (*PostStore, error) {
	store := &PostStore{
		posts:      make(map[domain.PostRef]domain.Post, len(seed)),
		threads:    make(map[domain.ThreadRef]struct{}, len(seed)),
		replayKeys: make(map[string]domain.PostRef),
		feed:       make([]domain.PostRef, 0, len(seed)),
		cursorID:   rand.Text(),
	}
	for _, post := range seed {
		if err := validatePost(post); err != nil {
			return nil, err
		}
		if err := store.insert(post); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *PostStore) GetPost(ctx context.Context, ref domain.PostRef) (domain.Post, error) {
	if err := ctx.Err(); err != nil {
		return domain.Post{}, err
	}
	if !validPostRef(ref) {
		return domain.Post{}, invalidPostArgument("Platform and external post ID are required")
	}
	s.mu.RLock()
	post, found := s.posts[ref]
	s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return domain.Post{}, err
	}
	if !found {
		return domain.Post{}, &contracts.Error{Code: contracts.NotFound, Message: "Post not found"}
	}
	return copyPost(post), nil
}

// CreatePost records the original post, its thread binding, and its replay key
// atomically in memory.
func (s *PostStore) CreatePost(ctx context.Context, post domain.Post, replayKey string) (domain.Post, error) {
	if err := ctx.Err(); err != nil {
		return domain.Post{}, err
	}
	if !validMarketplaceIdentifier(replayKey) {
		return domain.Post{}, invalidPostArgument("An idempotency key is required")
	}
	if err := validatePost(post); err != nil {
		return domain.Post{}, err
	}
	post.CreatedAt = post.CreatedAt.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Post{}, err
	}
	if ref, found := s.replayKeys[replayKey]; found {
		previous := s.posts[ref]
		if !reflect.DeepEqual(previous, post) {
			return domain.Post{}, &contracts.Error{Code: contracts.IdempotencyConflict, Message: "Idempotency key already used for another request"}
		}
		return copyPost(previous), nil
	}
	if err := s.insert(post); err != nil {
		return domain.Post{}, err
	}
	s.replayKeys[replayKey] = post.PostRef
	return copyPost(s.posts[post.PostRef]), nil
}

// insert runs while holding the write lock or while initializing the store.
func (s *PostStore) insert(post domain.Post) error {
	if _, found := s.posts[post.PostRef]; found {
		return &contracts.Error{Code: contracts.IdempotencyConflict, Message: "Post identity already exists"}
	}
	thread := domain.ThreadRef{Platform: post.Platform, ThreadID: post.ThreadID}
	if _, found := s.threads[thread]; found {
		return &contracts.Error{Code: contracts.IdempotencyConflict, Message: "Thread is bound to another post"}
	}
	post.CreatedAt = post.CreatedAt.UTC()
	s.posts[post.PostRef] = copyPost(post)
	s.threads[thread] = struct{}{}
	s.feed = append(s.feed, post.PostRef)
	return nil
}

type searchPostCursor struct {
	Query string
	City  string
	After domain.PostRef
}

func (s *PostStore) SearchPost(ctx context.Context, req contracts.SearchPostRequest) (contracts.SearchPostResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.SearchPostResult{}, err
	}
	limit, err := postPageSize(req.Limit)
	if err != nil {
		return contracts.SearchPostResult{}, err
	}
	if !utf8.ValidString(req.Query) || !utf8.ValidString(req.City) {
		return contracts.SearchPostResult{}, invalidPostArgument("Query and city must be valid UTF-8")
	}
	terms := strings.Fields(strings.ToLower(req.Query))
	slices.Sort(terms)
	terms = slices.Compact(terms)
	query := strings.Join(terms, " ")
	city := strings.ToLower(strings.TrimSpace(req.City))

	var after domain.PostRef
	prefix := s.cursorID + ".search."
	if req.Cursor != "" {
		token, ok := strings.CutPrefix(req.Cursor, prefix)
		payload, decodeErr := base64.RawURLEncoding.DecodeString(token)
		var cursor searchPostCursor
		if !ok || decodeErr != nil || json.Unmarshal(payload, &cursor) != nil ||
			cursor.Query != query || cursor.City != city || !validPostRef(cursor.After) {
			return contracts.SearchPostResult{}, invalidPostArgument("Invalid search cursor")
		}
		after = cursor.After
	}

	matching, err := s.matchingPosts(ctx, after, terms, city)
	if err != nil {
		return contracts.SearchPostResult{}, err
	}
	slices.SortFunc(matching, comparePosts)
	if err := ctx.Err(); err != nil {
		return contracts.SearchPostResult{}, err
	}

	var next string
	if len(matching) > limit {
		matching = matching[:limit]
		// A search cursor contains only strings and can always be marshaled.
		payload, _ := json.Marshal(searchPostCursor{Query: query, City: city, After: matching[limit-1].PostRef})
		next = prefix + base64.RawURLEncoding.EncodeToString(payload)
	}
	posts := make([]domain.Post, len(matching))
	for i, post := range matching {
		posts[i] = copyPost(post)
	}
	return contracts.SearchPostResult{Posts: posts, NextCursor: next}, nil
}

func (s *PostStore) matchingPosts(ctx context.Context, after domain.PostRef, terms []string, city string) ([]domain.Post, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var boundary domain.Post
	if after != (domain.PostRef{}) {
		var found bool
		boundary, found = s.posts[after]
		if !found || !postMatches(boundary, terms, city) {
			return nil, invalidPostArgument("Invalid search cursor")
		}
	}
	var matching []domain.Post
	for _, post := range s.posts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if after != (domain.PostRef{}) && comparePosts(post, boundary) <= 0 {
			continue
		}
		if postMatches(post, terms, city) {
			matching = append(matching, post)
		}
	}
	return matching, nil
}

func comparePosts(a, b domain.Post) int {
	if order := b.CreatedAt.Compare(a.CreatedAt); order != 0 {
		return order
	}
	if order := cmp.Compare(a.Platform, b.Platform); order != 0 {
		return order
	}
	return cmp.Compare(a.ExternalPostID, b.ExternalPostID)
}

func postMatches(post domain.Post, terms []string, city string) bool {
	if city != "" && strings.ToLower(strings.TrimSpace(post.Location.City)) != city {
		return false
	}
	if len(terms) == 0 {
		return true
	}
	text := strings.ToLower(post.Text)
	for _, term := range terms {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}

func (s *PostStore) ReadPosts(ctx context.Context, cursor string, limit int) (contracts.ReadPostsResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ReadPostsResult{}, err
	}
	limit, err := postPageSize(limit)
	if err != nil {
		return contracts.ReadPostsResult{}, err
	}
	prefix := s.cursorID + ".feed."
	offset := 0
	if cursor != "" {
		position, ok := strings.CutPrefix(cursor, prefix)
		offset, err = strconv.Atoi(position)
		if !ok || err != nil || offset < 0 {
			return contracts.ReadPostsResult{}, invalidPostArgument("Invalid post feed cursor")
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return contracts.ReadPostsResult{}, err
	}
	if offset > len(s.feed) {
		return contracts.ReadPostsResult{}, invalidPostArgument("Invalid post feed cursor")
	}
	count := min(limit, len(s.feed)-offset)
	posts := make([]domain.Post, count)
	for i, ref := range s.feed[offset : offset+count] {
		posts[i] = copyPost(s.posts[ref])
	}
	return contracts.ReadPostsResult{Posts: posts, NextCursor: prefix + strconv.Itoa(offset+count)}, nil
}

func postPageSize(limit int) (int, error) {
	switch {
	case limit == 0:
		return defaultPostPageSize, nil
	case limit < 0 || limit > maxPostPageSize:
		return 0, invalidPostArgument("Limit must be between 1 and 100, or zero for the default")
	default:
		return limit, nil
	}
}

func validatePost(post domain.Post) error {
	if !validPostRef(post.PostRef) || !validMarketplaceIdentifier(post.BuyerID) || !validMarketplaceIdentifier(post.ThreadID) {
		return invalidPostArgument("Platform, external post ID, buyer ID, and thread ID are required")
	}
	if strings.TrimSpace(post.Text) == "" || !utf8.ValidString(post.Text) || !utf8.ValidString(post.Location.City) {
		return invalidPostArgument("Post text is required and text and city must be valid UTF-8")
	}
	if post.CreatedAt.IsZero() || post.CreatedAt.UTC().Year() < 1 || post.CreatedAt.UTC().Year() > 9999 {
		return invalidPostArgument("Post creation timestamp is required")
	}
	if !validCoordinate(post.Location.Latitude, 90) || !validCoordinate(post.Location.Longitude, 180) {
		return invalidPostArgument("Post coordinates are out of range")
	}
	return nil
}

func validPostRef(ref domain.PostRef) bool {
	return validMarketplaceIdentifier(ref.Platform) && validMarketplaceIdentifier(ref.ExternalPostID)
}

func validMarketplaceIdentifier(id string) bool {
	return id != "" && strings.TrimSpace(id) == id && utf8.ValidString(id)
}

func validCoordinate(value *float64, limit float64) bool {
	return value == nil || math.Abs(*value) <= limit
}

func copyPost(post domain.Post) domain.Post {
	if post.Location.Latitude != nil {
		latitude := *post.Location.Latitude
		post.Location.Latitude = &latitude
	}
	if post.Location.Longitude != nil {
		longitude := *post.Location.Longitude
		post.Location.Longitude = &longitude
	}
	return post
}

func invalidPostArgument(message string) *contracts.Error {
	return &contracts.Error{Code: contracts.InvalidArgument, Message: message}
}
