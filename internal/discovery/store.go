package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func discoveryError(code contracts.Code, message string) error {
	return &contracts.Error{Code: code, Message: message}
}
func (s *Service) startRun(ctx context.Context, seller string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sellers SET name=name WHERE id=?`, seller)
	if err != nil {
		return "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", discoveryError(contracts.NotFound, "seller not found")
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM discovery_runs WHERE seller_id=? AND status='running'`, seller).Scan(&active); err != nil {
		return "", err
	}
	if active > 0 {
		return "", discoveryError(contracts.InvalidState, "discovery is already running for this seller")
	}
	id := s.opts.NewID()
	if _, err = tx.ExecContext(ctx, `INSERT INTO discovery_runs(id,seller_id,status,created_at) VALUES(?,?,'running',?)`, id, seller, s.opts.Now().UnixMilli()); err != nil {
		return "", err
	}
	return id, tx.Commit()
}
func (s *Service) finishRun(ctx context.Context, id, status string, result Result) error {
	if status != "completed" && status != "partial" && status != "failed" {
		return errors.New("invalid discovery completion status")
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE discovery_runs SET status=?,result_json=?,finished_at=? WHERE id=? AND status='running'`, status, string(data), s.opts.Now().UnixMilli(), id)
	return err
}
func (s *Service) loadInventory(ctx context.Context, seller string) ([]domain.Item, bool, error) {
	now := s.opts.Now().UnixMilli()
	rows, err := s.db.QueryContext(ctx, `SELECT i.id FROM inventory_items i WHERE i.seller_id=? AND i.active=1 AND i.physical_stock>COALESCE((SELECT sum(r.quantity) FROM reservations r WHERE r.seller_id=i.seller_id AND r.item_id=i.id AND r.status='active' AND r.expires_at>?),0) ORDER BY i.id LIMIT ?`, seller, now, s.opts.MaxItems+1)
	if err != nil {
		return nil, false, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, false, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	truncated := len(ids) > s.opts.MaxItems
	if truncated {
		ids = ids[:s.opts.MaxItems]
	}
	items := make([]domain.Item, 0, len(ids))
	for _, id := range ids {
		item, err := s.commerce.GetItem(ctx, seller, id)
		if err != nil {
			return nil, truncated, err
		}
		if item.SellerID != seller || item.ID != id {
			return nil, truncated, discoveryError(contracts.InvalidState, "inventory scope mismatch")
		}
		if item.Active && item.AvailableStock > 0 {
			items = append(items, item)
		}
	}
	return items, truncated, nil
}
func (s *Service) recordQuery(ctx context.Context, runID string, index int, query SearchQuery) (string, error) {
	source, err := json.Marshal(query.ItemIDs)
	if err != nil {
		return "", err
	}
	normalized := strings.Join(strings.Fields(strings.ToLower(query.Query)), " ")
	var previous string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM discovery_queries WHERE run_id=? AND normalized_query=?`, runID, normalized).Scan(&previous)
	if err == nil {
		return previous, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id := s.opts.NewID()
	_, err = s.db.ExecContext(ctx, `INSERT INTO discovery_queries(id,run_id,ordinal,query,normalized_query,source_item_ids_json,created_at) VALUES(?,?,?,?,?,?,?)`, id, runID, index, query.Query, normalized, string(source), s.opts.Now().UnixMilli())
	return id, err
}
func (s *Service) recordSearchPage(ctx context.Context, runID, queryID string, posts []domain.Post, nextCursor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, post := range posts {
		data, err := json.Marshal(post)
		if err != nil {
			return err
		}
		id := s.opts.NewID()
		if _, err = tx.ExecContext(ctx, `INSERT INTO discovery_posts(id,run_id,platform,external_post_id,post_json,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(run_id,platform,external_post_id) DO NOTHING`, id, runID, post.Platform, post.ExternalPostID, string(data), s.opts.Now().UnixMilli()); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, `SELECT id FROM discovery_posts WHERE run_id=? AND platform=? AND external_post_id=?`, runID, post.Platform, post.ExternalPostID).Scan(&id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO discovery_query_posts(run_id,query_id,post_id) VALUES(?,?,?) ON CONFLICT(query_id,post_id) DO NOTHING`, runID, queryID, id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE discovery_queries SET next_cursor=?,pages_fetched=pages_fetched+1 WHERE id=? AND run_id=?`, nextCursor, queryID, runID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) recordMatch(ctx context.Context, runID string, post domain.Post, candidate ItemMatch, status, reason, leadID string) error {
	score := candidate.Score
	if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
		score = 0
	}
	var lead any
	if leadID != "" {
		lead = leadID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO discovery_matches(id,run_id,platform,external_post_id,item_id,score,reason,status,decision_reason,lead_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id,platform,external_post_id,item_id) DO UPDATE SET score=excluded.score,reason=excluded.reason,status=excluded.status,decision_reason=excluded.decision_reason,lead_id=excluded.lead_id`, s.opts.NewID(), runID, post.Platform, post.ExternalPostID, candidate.ItemID, score, candidate.Reason, status, reason, lead, s.opts.Now().UnixMilli())
	return err
}
func (s *Service) dispatchLead(ctx context.Context, leadID string) (string, error) {
	var event, status string
	err := s.db.QueryRowContext(ctx, `SELECT event_id,status FROM runtime_notifications WHERE lead_id=?`, leadID).Scan(&event, &status)
	if err != nil {
		return "failed", err
	}
	if status == "accepted" {
		return status, nil
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE runtime_notifications SET attempts=attempts+1,updated_at=? WHERE event_id=? AND status!='accepted'`, s.opts.Now().UnixMilli(), event); err != nil {
		return "failed", err
	}
	notifyErr := s.notifier.OnLeadDiscovered(ctx, event, leadID)
	// Retain the result after request cancellation, but never extend model/search
	// work. Stable event IDs make an uncertain acceptance safe to retry in-process.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if notifyErr != nil {
		_, err = s.db.ExecContext(saveCtx, `UPDATE runtime_notifications SET status='failed',error_code='NOTIFY_FAILED',error_message='runtime notification was not accepted',updated_at=? WHERE event_id=? AND status!='accepted'`, s.opts.Now().UnixMilli(), event)
		if err != nil {
			return "failed", err
		}
		return "failed", notifyErr
	}
	_, err = s.db.ExecContext(saveCtx, `UPDATE runtime_notifications SET status='accepted',accepted_at=?,error_code='',error_message='',updated_at=? WHERE event_id=?`, s.opts.Now().UnixMilli(), s.opts.Now().UnixMilli(), event)
	if err != nil {
		return "pending", err
	}
	return "accepted", nil
}

// RecoverInterruptedRuns is called once at process startup before serving work,
// never by each constructor/request (which could invalidate an active run).
func (s *Service) RecoverInterruptedRuns(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE discovery_runs SET status='failed',finished_at=? WHERE status='running'`, s.opts.Now().UnixMilli())
	return err
}

// recordPostDecision preserves extraction evidence even for nonbuying/no-match
// posts, so provider failures cannot be confused with a valid ignored result.
func (s *Service) recordPostDecision(ctx context.Context, runID string, post domain.Post, output PostMatch, status, code string) error {
	if status != "completed" && status != "failed" {
		return errors.New("invalid matching status")
	}
	var data any
	if status == "completed" {
		raw, err := json.Marshal(output)
		if err != nil {
			return err
		}
		data = string(raw)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE discovery_posts SET matching_status=?,intent_json=?,decision_code=? WHERE run_id=? AND platform=? AND external_post_id=?`, status, data, code, runID, post.Platform, post.ExternalPostID)
	return err
}
