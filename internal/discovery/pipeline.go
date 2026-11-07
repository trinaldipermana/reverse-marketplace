package discovery

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func dependencyFailure(ctx context.Context, message string, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &contracts.Error{Code: contracts.DependencyUnavailable, Message: message, Retryable: true, Cause: cause}
}
func addWarning(result *Result, code, message string) {
	result.Warnings = append(result.Warnings, Warning{Code: code, Message: message})
}

// Discover executes one bounded synchronous pipeline. Committed leads remain
// authoritative if later work fails; no message is sent by this component.
func (s *Service) Discover(parent context.Context, seller string) (result Result, err error) {
	result = Result{Matches: []MatchView{}, Warnings: []Warning{}}
	if !validDiscoveryIdentity(seller) {
		return result, discoveryError(contracts.InvalidArgument, "sellerId is required and must be valid")
	}
	ctx, cancel := context.WithTimeout(parent, s.opts.Timeout)
	defer cancel()
	runID, err := s.startRun(ctx, seller)
	if err != nil {
		return result, err
	}
	result.RunID = runID
	defer func() {
		status := "completed"
		if err != nil {
			status = "failed"
		} else if result.Truncated || len(result.Warnings) > 0 {
			status = "partial"
		}
		finishCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		if finishErr := s.finishRun(finishCtx, runID, status, result); finishErr != nil && err == nil {
			err = dependencyFailure(finishCtx, "discovery result could not be saved", finishErr)
		}
	}()
	items, truncated, err := s.loadInventory(ctx, seller)
	if err != nil {
		return result, dependencyFailure(ctx, "inventory could not be loaded", err)
	}
	result.ListingsScanned = len(items)
	result.Truncated = truncated
	if truncated {
		addWarning(&result, "INVENTORY_TRUNCATED", "Only the configured inventory shortlist was considered")
	}
	if len(items) == 0 {
		return result, nil
	}
	allowed := make(map[string]bool, len(items))
	for _, item := range items {
		allowed[item.ID] = true
	}
	planned, err := s.planner.PlanQueries(ctx, items, s.opts.MaxQueries)
	if err != nil {
		return result, dependencyFailure(ctx, "search queries could not be generated", err)
	}
	queries, invalid, queryTruncated := prepareQueries(planned, allowed, s.opts.MaxQueries)
	if invalid {
		addWarning(&result, "INVALID_QUERY", "An unusable generated query was skipped")
	}
	if queryTruncated {
		result.Truncated = true
		addWarning(&result, "QUERY_TRUNCATED", "Generated queries exceeded the configured budget")
	}
	if len(queries) == 0 {
		return result, dependencyFailure(ctx, "query generation produced no usable searches", nil)
	}
	posts := []domain.Post{}
	postTruncated := false
	observed := map[domain.PostRef]domain.Post{}
	successfulSearches := 0
	for index, query := range queries {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		queryID, saveErr := s.recordQuery(ctx, runID, index, query)
		if saveErr != nil {
			return result, dependencyFailure(ctx, "search query could not be saved", saveErr)
		}
		cursor := ""
		cursors := map[string]bool{"": true}
		for page := 0; page < s.opts.MaxPagesPerQuery; page++ {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			searchCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			found, searchErr := s.marketplace.SearchPost(searchCtx, contracts.SearchPostRequest{Query: query.Query, Cursor: cursor, Limit: s.opts.PageSize})
			stop()
			if searchErr != nil {
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				addWarning(&result, "SEARCH_FAILED", "A marketplace search could not be completed")
				break
			}
			successfulSearches++
			pagePosts := found.Posts
			if len(pagePosts) > s.opts.PageSize {
				pagePosts = pagePosts[:s.opts.PageSize]
				result.Truncated = true
				addWarning(&result, "SEARCH_TRUNCATED", "Marketplace results exceeded the requested page size")
			}
			canonical := make([]domain.Post, 0, len(pagePosts))
			for _, post := range pagePosts {
				if !validPost(post) {
					addWarning(&result, "INVALID_POST", "An unusable marketplace post was skipped")
					continue
				}
				if previous, ok := observed[post.PostRef]; ok {
					canonical = append(canonical, previous)
					continue
				}
				observed[post.PostRef] = post
				canonical = append(canonical, post)
				if len(posts) < s.opts.MaxPosts {
					posts = append(posts, post)
					result.RequestsScanned = len(posts)
				} else if !postTruncated {
					postTruncated = true
					result.Truncated = true
					addWarning(&result, "POSTS_TRUNCATED", "Only the configured number of unique posts was matched")
				}
			}
			if saveErr = s.recordSearchPage(ctx, runID, queryID, canonical, found.NextCursor); saveErr != nil {
				return result, dependencyFailure(ctx, "search provenance could not be saved", saveErr)
			}
			if found.NextCursor == "" {
				break
			}
			if cursors[found.NextCursor] {
				addWarning(&result, "CURSOR_LOOP", "A repeated marketplace cursor stopped this query")
				result.Truncated = true
				break
			}
			cursors[found.NextCursor] = true
			cursor = found.NextCursor
			if page+1 == s.opts.MaxPagesPerQuery {
				result.Truncated = true
				addWarning(&result, "PAGES_TRUNCATED", "Additional marketplace pages were outside the search budget")
			}
		}
	}
	if successfulSearches == 0 {
		return result, dependencyFailure(ctx, "all marketplace searches failed", nil)
	}
	result.RequestsScanned = len(posts)
	successfulMatches := 0
	for _, post := range posts {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		matched, matchErr := s.matcher.MatchPost(ctx, post, items)
		if matchErr != nil {
			if saveErr := s.recordPostDecision(ctx, runID, post, PostMatch{}, "failed", "MATCH_FAILED"); saveErr != nil && ctx.Err() == nil {
				return result, dependencyFailure(ctx, "post evaluation could not be saved", saveErr)
			}
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			addWarning(&result, "MATCH_FAILED", "A post could not be evaluated")
			continue
		}
		if !validIntent(matched.Intent) {
			if saveErr := s.recordPostDecision(ctx, runID, post, PostMatch{}, "failed", "INVALID_INTENT"); saveErr != nil {
				return result, dependencyFailure(ctx, "post evaluation could not be saved", saveErr)
			}
			addWarning(&result, "INVALID_INTENT", "A post returned unusable buying intent")
			continue
		}
		if saveErr := s.recordPostDecision(ctx, runID, post, matched, "completed", "EXTRACTED"); saveErr != nil {
			return result, dependencyFailure(ctx, "post evaluation could not be saved", saveErr)
		}
		successfulMatches++
		if !matched.Intent.IsBuyingRequest || matched.Intent.Confidence < s.opts.IntentThreshold {
			continue
		}
		if matched.Intent.Qty == 0 {
			matched.Intent.Qty = 1
		}
		candidates := []eligibleCandidate{}
		seenItems := map[string]bool{}
		for _, candidate := range matched.Matches {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			if seenItems[candidate.ItemID] {
				continue
			}
			seenItems[candidate.ItemID] = true
			eligible, rejection, validateErr := s.validateCandidate(ctx, seller, post, matched, candidate, allowed)
			if validateErr != nil {
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				addWarning(&result, "VALIDATION_FAILED", "An inventory candidate could not be validated")
				continue
			}
			if rejection != "" {
				if saveErr := s.recordMatch(ctx, runID, post, candidate, "rejected", rejection, ""); saveErr != nil {
					return result, dependencyFailure(ctx, "match decision could not be saved", saveErr)
				}
				continue
			}
			candidates = append(candidates, eligible)
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			if a.Match.Score != b.Match.Score {
				return a.Match.Score > b.Match.Score
			}
			if a.DistanceKM != b.DistanceKM {
				return a.DistanceKM < b.DistanceKM
			}
			if a.Item.ListPriceIDR != b.Item.ListPriceIDR {
				return a.Item.ListPriceIDR < b.Item.ListPriceIDR
			}
			return a.Item.ID < b.Item.ID
		})
		if len(candidates) == 0 {
			continue
		}
		chosen := candidates[0]
		alternatives := make([]string, 0, len(candidates)-1)
		for _, alternative := range candidates[1:] {
			alternatives = append(alternatives, alternative.Item.ID)
		}
		lead, _, leadErr := s.commerce.CreateLead(ctx, commerce.CreateLeadInput{SellerID: seller, Platform: post.Platform, PostID: post.ExternalPostID, BuyerID: post.BuyerID, ThreadID: post.ThreadID, ItemID: chosen.Item.ID, Intent: matched.Intent, MatchReason: chosen.Match.Reason, AlternativeItemIDs: alternatives})
		if leadErr != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			addWarning(&result, "LEAD_FAILED", "A matched lead could not be created")
			continue
		}
		if lead.SellerID != seller || lead.Platform != post.Platform || lead.PostID != post.ExternalPostID || lead.BuyerID != post.BuyerID || lead.ThreadID != post.ThreadID {
			addWarning(&result, "LEAD_SCOPE", "A lead returned inconsistent marketplace identity")
			continue
		}
		// Existing leads may preserve a different primary item from an earlier run;
		// display that authoritative item rather than imply terms were overwritten.
		if lead.ItemID != chosen.Item.ID {
			currentItem, readErr := s.commerce.GetItem(ctx, seller, lead.ItemID)
			if readErr != nil || currentItem.SellerID != seller || currentItem.ID != lead.ItemID {
				addWarning(&result, "LEAD_ITEM_FAILED", "An existing lead item could not be loaded")
				continue
			}
			if s.opts.FixedPrice {
				rechecked, rejection, checkErr := s.validateCandidate(ctx, seller, post, matched, ItemMatch{ItemID: currentItem.ID, Score: 1, Reason: "Existing lead item"}, allowed)
				if checkErr != nil || rejection != "" {
					addWarning(&result, "EXISTING_LEAD_INELIGIBLE", "Existing lead does not meet fixed-price listing requirements")
					continue
				}
				chosen.PartnerURL = rechecked.PartnerURL
			}
			chosen.Item = currentItem
			chosen.Match = ItemMatch{ItemID: currentItem.ID, Score: 0, Reason: "Existing lead kept its previously selected item"}
			addWarning(&result, "EXISTING_LEAD", "An existing lead kept its previously selected item")
		}
		for i, candidate := range candidates {
			status := "alternative"
			if i == 0 {
				status = "selected"
			}
			if saveErr := s.recordMatch(ctx, runID, post, candidate.Match, status, "", lead.ID); saveErr != nil {
				return result, dependencyFailure(ctx, "selected match could not be saved", saveErr)
			}
		}
		notification, notifyErr := s.dispatchLead(ctx, lead.ID)
		if notifyErr != nil {
			addWarning(&result, "NOTIFY_FAILED", "The lead is saved but runtime acceptance is pending")
		}
		result.Matches = append(result.Matches, matchView(seller, post, chosen, lead, notification))
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
	}
	if len(posts) > 0 && successfulMatches == 0 {
		return result, dependencyFailure(ctx, "all post evaluations failed", nil)
	}
	return result, nil
}

func prepareQueries(planned []SearchQuery, allowed map[string]bool, max int) ([]SearchQuery, bool, bool) {
	queries := []SearchQuery{}
	byQuery := map[string]int{}
	invalid, truncated := false, false
	for _, query := range planned {
		if !utf8.ValidString(query.Query) || len(query.Query) > 200 || len(query.ItemIDs) == 0 {
			invalid = true
			continue
		}
		terms := strings.Fields(strings.ToLower(query.Query))
		sort.Strings(terms)
		unique := terms[:0]
		for _, term := range terms {
			if len(unique) == 0 || unique[len(unique)-1] != term {
				unique = append(unique, term)
			}
		}
		normalized := strings.Join(unique, " ")
		if normalized == "" {
			invalid = true
			continue
		}
		ids := []string{}
		seen := map[string]bool{}
		valid := true
		for _, id := range query.ItemIDs {
			if !allowed[id] {
				valid = false
				break
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		if !valid {
			invalid = true
			continue
		}
		sort.Strings(ids)
		if index, exists := byQuery[normalized]; exists {
			for _, id := range ids {
				found := false
				for _, existing := range queries[index].ItemIDs {
					if existing == id {
						found = true
						break
					}
				}
				if !found {
					queries[index].ItemIDs = append(queries[index].ItemIDs, id)
				}
			}
			sort.Strings(queries[index].ItemIDs)
			continue
		}
		if len(queries) >= max {
			truncated = true
			continue
		}
		byQuery[normalized] = len(queries)
		queries = append(queries, SearchQuery{Query: normalized, ItemIDs: ids})
	}
	return queries, invalid, truncated
}
