package mock_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/mock"
)

var fixtureTime = time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)

func post(platform, id, city, body string, created time.Time) domain.Post {
	return domain.Post{
		PostRef: domain.PostRef{Platform: platform, ExternalPostID: id},
		BuyerID: "buyer-" + id, ThreadID: "thread-" + id, Text: body, CreatedAt: created,
		Location: domain.Location{City: city},
	}
}

func TestPostSearchContract(t *testing.T) {
	ctx := context.Background()
	buying := post("mock", "a", "Jakarta", "Butuh LaPTop bekas untuk coding", fixtureTime)
	selling := post("mock", "b", "Bandung", "Dijual laptop bekas", fixtureTime)
	unknown := post("mock", "c", "", "Need a laptop", fixtureTime.Add(-time.Hour))
	otherPlatform := post("other", "a", "Jakarta", "Need a desk", fixtureTime.Add(time.Hour))
	store := newStore(t, unknown, selling, buying, otherPlatform)
	var discovery contracts.MarketplaceDiscovery = store

	t.Run("canonical lookup and overlapping queries", func(t *testing.T) {
		found := search(t, discovery, contracts.SearchPostRequest{Query: "  CODING LAPTOP "})
		assertRefs(t, found.Posts, buying)
		overlap := search(t, discovery, contracts.SearchPostRequest{Query: "laptop bekas"})
		assertRefs(t, overlap.Posts, buying, selling)
		got, err := discovery.GetPost(ctx, found.Posts[0].PostRef)
		if err != nil || got != buying {
			t.Fatalf("GetPost = %+v, %v, want %+v", got, err, buying)
		}
		got, err = discovery.GetPost(ctx, otherPlatform.PostRef)
		if err != nil || got != otherPlatform {
			t.Fatalf("GetPost on other platform = %+v, %v, want %+v", got, err, otherPlatform)
		}
	})

	t.Run("city, unknown location, and empty results", func(t *testing.T) {
		filtered := search(t, discovery, contracts.SearchPostRequest{Query: "laptop", City: " JAKARTA "})
		assertRefs(t, filtered.Posts, buying)
		unfiltered := search(t, discovery, contracts.SearchPostRequest{Query: "laptop"})
		assertRefs(t, unfiltered.Posts, buying, selling, unknown)
		empty := search(t, discovery, contracts.SearchPostRequest{Query: "not-in-fixture"})
		if empty.Posts == nil || len(empty.Posts) != 0 || empty.NextCursor != "" {
			t.Fatalf("empty search = %+v, want a non-nil empty slice and no cursor", empty)
		}
	})

	t.Run("empty query and stable search pagination", func(t *testing.T) {
		first := search(t, discovery, contracts.SearchPostRequest{Limit: 2})
		assertRefs(t, first.Posts, otherPlatform, buying)
		if first.NextCursor == "" {
			t.Fatal("first page has no next cursor")
		}
		last := search(t, discovery, contracts.SearchPostRequest{Cursor: first.NextCursor, Limit: 2})
		assertRefs(t, last.Posts, selling, unknown)
		if last.NextCursor != "" {
			t.Fatal("last page has a next cursor")
		}
	})

	t.Run("cursor bound to normalized filters", func(t *testing.T) {
		first := search(t, discovery, contracts.SearchPostRequest{Query: "laptop bekas", Limit: 1})
		assertRefs(t, first.Posts, buying)
		last := search(t, discovery, contracts.SearchPostRequest{Query: " BEKAS  LAPTOP laptop", Cursor: first.NextCursor})
		assertRefs(t, last.Posts, selling)
		_, err := discovery.SearchPost(ctx, contracts.SearchPostRequest{Query: "laptop", Cursor: first.NextCursor})
		assertCode(t, err, contracts.InvalidArgument)
		_, err = discovery.SearchPost(ctx, contracts.SearchPostRequest{Query: "laptop bekas", City: "Jakarta", Cursor: first.NextCursor})
		assertCode(t, err, contracts.InvalidArgument)
	})

	t.Run("invalid, missing, and canceled are distinct", func(t *testing.T) {
		_, err := discovery.GetPost(ctx, domain.PostRef{Platform: "other", ExternalPostID: "missing"})
		assertCode(t, err, contracts.NotFound)
		_, err = discovery.GetPost(ctx, domain.PostRef{ExternalPostID: "a"})
		assertCode(t, err, contracts.InvalidArgument)
		for _, req := range []contracts.SearchPostRequest{{Limit: -1}, {Limit: 101}, {Cursor: "nonsense"}, {Query: "\xff"}} {
			_, err := discovery.SearchPost(ctx, req)
			assertCode(t, err, contracts.InvalidArgument)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = discovery.SearchPost(canceled, contracts.SearchPostRequest{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled search = %v", err)
		}
		expired, stop := context.WithDeadline(ctx, time.Unix(0, 0))
		defer stop()
		_, err = discovery.GetPost(expired, buying.PostRef)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired lookup = %v", err)
		}
	})
}

func TestPostSearchConcurrentInsertsUseCursorBoundary(t *testing.T) {
	ctx := context.Background()
	older := post("mock", "a", "", "old", fixtureTime.Add(-time.Hour))
	newer := post("mock", "b", "", "new", fixtureTime)
	store := newStore(t, older, newer)
	first := search(t, store, contracts.SearchPostRequest{Limit: 1})
	assertRefs(t, first.Posts, newer)

	ahead := post("mock", "c", "", "ahead", fixtureTime.Add(time.Hour))
	behind := post("mock", "d", "", "behind", fixtureTime.Add(-time.Minute))
	for _, inserted := range []domain.Post{ahead, behind} {
		if _, err := store.CreatePost(ctx, inserted, inserted.ExternalPostID); err != nil {
			t.Fatal(err)
		}
	}
	next := search(t, store, contracts.SearchPostRequest{Cursor: first.NextCursor})
	assertRefs(t, next.Posts, behind, older)
	fresh := search(t, store, contracts.SearchPostRequest{})
	assertRefs(t, fresh.Posts, ahead, newer, behind, older)
}

func TestPostFeedResumesAfterExhaustionAndIncludesBackdatedInserts(t *testing.T) {
	ctx := context.Background()
	one := post("mock", "one", "", "first", fixtureTime)
	two := post("mock", "two", "", "second", fixtureTime.Add(-time.Hour))
	three := post("mock", "three", "", "third", fixtureTime.Add(-2*time.Hour))
	store := newStore(t, one, two)
	var feed contracts.PostFeed = store
	first, err := feed.ReadPosts(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	assertRefs(t, first.Posts, one)
	last, err := feed.ReadPosts(ctx, first.NextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertRefs(t, last.Posts, two)
	empty, err := feed.ReadPosts(ctx, last.NextCursor, 1)
	if err != nil || empty.Posts == nil || len(empty.Posts) != 0 || empty.NextCursor != last.NextCursor {
		t.Fatalf("exhausted feed = %+v, %v", empty, err)
	}

	if _, err := store.CreatePost(ctx, three, "key"); err != nil {
		t.Fatal(err)
	}
	resumed, err := feed.ReadPosts(ctx, empty.NextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertRefs(t, resumed.Posts, three)
	_, err = store.SearchPost(ctx, contracts.SearchPostRequest{Cursor: resumed.NextCursor})
	assertCode(t, err, contracts.InvalidArgument)
	searchPage := search(t, store, contracts.SearchPostRequest{Limit: 1})
	_, err = feed.ReadPosts(ctx, searchPage.NextCursor, 1)
	assertCode(t, err, contracts.InvalidArgument)
	_, err = feed.ReadPosts(ctx, "", -1)
	assertCode(t, err, contracts.InvalidArgument)
	newGeneration := newStore(t, one, two, three)
	_, err = newGeneration.ReadPosts(ctx, first.NextCursor, 1)
	assertCode(t, err, contracts.InvalidArgument)
	_, err = newGeneration.SearchPost(ctx, contracts.SearchPostRequest{Cursor: searchPage.NextCursor})
	assertCode(t, err, contracts.InvalidArgument)
}

func TestPostStoreCanonicalThreadBinding(t *testing.T) {
	ctx := context.Background()
	first := post("mock", "first", "", "first post", fixtureTime)
	second := post("mock", "second", "", "conflicting post", fixtureTime)
	second.ThreadID, second.BuyerID = first.ThreadID, first.BuyerID
	otherPlatform := second
	otherPlatform.Platform = "other"

	t.Run("seed binds one post per platform and thread", func(t *testing.T) {
		_, err := mock.NewPostStore(first, second)
		assertCode(t, err, contracts.IdempotencyConflict)
		store := newStore(t, first, otherPlatform)
		feed, err := store.ReadPosts(ctx, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		assertRefs(t, feed.Posts, first, otherPlatform)
	})

	t.Run("create rejects a conflicting binding without reserving the key", func(t *testing.T) {
		store := newStore(t)
		if got, err := store.CreatePost(ctx, first, "first-key"); err != nil || got != first {
			t.Fatalf("create first = %+v, %v", got, err)
		}
		_, err := store.CreatePost(ctx, second, "second-key")
		assertCode(t, err, contracts.IdempotencyConflict)
		_, err = store.GetPost(ctx, second.PostRef)
		assertCode(t, err, contracts.NotFound)

		if got, err := store.CreatePost(ctx, otherPlatform, "second-key"); err != nil || got != otherPlatform {
			t.Fatalf("create on other platform using unreserved key = %+v, %v", got, err)
		}
		if got, err := store.CreatePost(ctx, first, "first-key"); err != nil || got != first {
			t.Fatalf("identical replay = %+v, %v", got, err)
		}
		feed, err := store.ReadPosts(ctx, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		assertRefs(t, feed.Posts, first, otherPlatform)

		threads := mock.NewThreads(store, nil)
		for _, saved := range feed.Posts {
			if err := threads.OpenThread(ctx, saved.PostRef); err != nil {
				t.Fatalf("open saved post's thread %v: %v", saved.PostRef, err)
			}
		}
	})
}

func TestPostCreateConcurrentReplaysWriteOnce(t *testing.T) {
	ctx := context.Background()
	entry := post("mock", "post-1", "Jakarta", "  Original post text\n", fixtureTime)
	store := newStore(t)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			got, err := store.CreatePost(ctx, entry, "same-request")
			if err != nil || got != entry {
				t.Errorf("CreatePost = %+v, %v, want %+v", got, err, entry)
			}
		})
	}
	wg.Wait()
	page, err := store.ReadPosts(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertRefs(t, page.Posts, entry)

	changed := entry
	changed.Text = "different"
	_, err = store.CreatePost(ctx, changed, "same-request")
	assertCode(t, err, contracts.IdempotencyConflict)
	_, err = store.CreatePost(ctx, entry, "new-request")
	assertCode(t, err, contracts.IdempotencyConflict)
	_, err = mock.NewPostStore(entry, entry)
	assertCode(t, err, contracts.IdempotencyConflict)
}

func TestPostStoreCopiesOptionalCoordinates(t *testing.T) {
	ctx := context.Background()
	entry := post("mock", "post-1", "Jakarta", "sample", fixtureTime)
	latitude := -6.2
	entry.Location.Latitude = &latitude
	store := newStore(t, entry)
	latitude = 0
	found := search(t, store, contracts.SearchPostRequest{})
	assertRefs(t, found.Posts, entry)
	if found.Posts[0].Location.Latitude == nil || *found.Posts[0].Location.Latitude != -6.2 {
		t.Fatalf("seed location was mutated: %+v", found.Posts[0].Location)
	}
	*found.Posts[0].Location.Latitude = 2
	current, err := store.GetPost(ctx, entry.PostRef)
	if err != nil || current.Location.Latitude == nil || *current.Location.Latitude != -6.2 {
		t.Fatalf("stored location was mutated: %+v, %v", current.Location, err)
	}
}

func TestPostCreateRejectsInvalidInputsWithoutUsingReplayKey(t *testing.T) {
	ctx := context.Background()
	entry := post("mock", "post-1", "", "sample", fixtureTime)
	store := newStore(t)
	invalid := entry
	invalid.CreatedAt = time.Time{}
	_, err := store.CreatePost(ctx, invalid, "reusable-key")
	assertCode(t, err, contracts.InvalidArgument)
	_, err = store.CreatePost(ctx, entry, " ")
	assertCode(t, err, contracts.InvalidArgument)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.CreatePost(canceled, entry, "reusable-key")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled create = %v", err)
	}
	if _, err := store.CreatePost(ctx, entry, "reusable-key"); err != nil {
		t.Fatal(err)
	}
}

func newStore(t *testing.T, seed ...domain.Post) *mock.PostStore {
	t.Helper()
	store, err := mock.NewPostStore(seed...)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func search(t *testing.T, store contracts.MarketplaceDiscovery, req contracts.SearchPostRequest) contracts.SearchPostResult {
	t.Helper()
	result, err := store.SearchPost(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertRefs(t *testing.T, posts []domain.Post, expected ...domain.Post) {
	t.Helper()
	refs := func(posts []domain.Post) []domain.PostRef {
		refs := make([]domain.PostRef, 0, len(posts))
		for _, p := range posts {
			refs = append(refs, p.PostRef)
		}
		return refs
	}
	if got, want := refs(posts), refs(expected); !slices.Equal(got, want) {
		t.Fatalf("post refs = %v, want %v", got, want)
	}
}

func assertCode(t *testing.T, err error, want contracts.Code) {
	t.Helper()
	var coded *contracts.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}
