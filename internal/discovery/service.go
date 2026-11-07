package discovery

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
)

// New injects already-shared commerce, marketplace and model services. It does
// not create another database, send messages or start background discovery.
func New(db *sql.DB, commerce Commerce, marketplace contracts.MarketplaceDiscovery, planner QueryPlanner, matcher PostMatcher, notifier LeadNotifier, opts Options) (*Service, error) {
	if db == nil || commerce == nil || marketplace == nil || planner == nil || matcher == nil || notifier == nil {
		return nil, errors.New("discovery requires database, commerce, marketplace, planner, matcher and notifier")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = func() string {
			var b [16]byte
			if _, err := rand.Read(b[:]); err != nil {
				panic(err)
			}
			return hex.EncodeToString(b[:])
		}
	}
	if opts.Timeout == 0 {
		opts.Timeout = 90 * time.Second
	}
	if opts.MaxQueries == 0 {
		opts.MaxQueries = 4
	}
	if opts.PageSize == 0 {
		opts.PageSize = 20
	}
	if opts.MaxPagesPerQuery == 0 {
		opts.MaxPagesPerQuery = 1
	}
	if opts.MaxPosts == 0 {
		opts.MaxPosts = 20
	}
	if opts.MaxItems == 0 {
		opts.MaxItems = 30
	}
	if opts.IntentThreshold == 0 {
		opts.IntentThreshold = .8
	}
	if opts.MatchThreshold == 0 {
		opts.MatchThreshold = .75
	}
	if opts.MaxDistanceKM == 0 {
		opts.MaxDistanceKM = 50
	}
	if opts.Timeout <= 0 || opts.Timeout > 5*time.Minute || opts.MaxQueries < 1 || opts.MaxQueries > 20 || opts.PageSize < 1 || opts.PageSize > 100 || opts.MaxPagesPerQuery < 1 || opts.MaxPagesPerQuery > 10 || opts.MaxPosts < 1 || opts.MaxPosts > 200 || opts.MaxItems < 1 || opts.MaxItems > 100 || math.IsNaN(opts.IntentThreshold) || opts.IntentThreshold < 0 || opts.IntentThreshold > 1 || math.IsNaN(opts.MatchThreshold) || opts.MatchThreshold < 0 || opts.MatchThreshold > 1 || math.IsNaN(opts.MaxDistanceKM) || math.IsInf(opts.MaxDistanceKM, 0) || opts.MaxDistanceKM < 0 {
		return nil, errors.New("invalid discovery limits")
	}
	opts.PartnerHosts = append([]string(nil), opts.PartnerHosts...)
	return &Service{db: db, commerce: commerce, marketplace: marketplace, planner: planner, matcher: matcher, notifier: notifier, opts: opts}, nil
}
