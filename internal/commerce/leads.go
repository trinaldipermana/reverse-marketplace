package commerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

type CreateLeadInput struct {
	SellerID           string
	Platform           string
	PostID             string
	BuyerID            string
	ThreadID           string
	ItemID             string
	Intent             domain.Intent
	MatchReason        string
	AlternativeItemIDs []string
}

func validIdentity(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 200
}

// CreateLead persists one lead and one initial notification intent atomically.
// The bool reports newly created; discovery notifies P1 only for new leads.
// Existing leads are returned unchanged, including terminal/escalated leads.
func (s *Service) CreateLead(ctx context.Context, in CreateLeadInput) (domain.LeadRecord, bool, error) {
	for _, id := range []string{in.SellerID, in.Platform, in.PostID, in.BuyerID, in.ThreadID, in.ItemID} {
		if !validIdentity(id) {
			return domain.LeadRecord{}, false, commerceProblem(contracts.InvalidArgument, "lead identities are required and must be valid")
		}
	}
	if in.Intent.Qty == 0 {
		in.Intent.Qty = 1
	}
	if !in.Intent.IsBuyingRequest || in.Intent.Qty != 1 || math.IsNaN(in.Intent.Confidence) || math.IsInf(in.Intent.Confidence, 0) || in.Intent.Confidence < 0 || in.Intent.Confidence > 1 || strings.TrimSpace(in.Intent.ItemQuery) == "" || strings.TrimSpace(in.MatchReason) == "" || len(in.MatchReason) > 2000 || len(in.AlternativeItemIDs) > 30 {
		return domain.LeadRecord{}, false, commerceProblem(contracts.InvalidArgument, "invalid buying intent or match evidence")
	}
	if in.Intent.BudgetIDR != nil && *in.Intent.BudgetIDR <= 0 {
		return domain.LeadRecord{}, false, commerceProblem(contracts.InvalidArgument, "budget must be positive when specified")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return domain.LeadRecord{}, false, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM leads WHERE seller_id=? AND platform=? AND post_id=?`, in.SellerID, in.Platform, in.PostID).Scan(&existing)
	if err == nil {
		lead, err := s.leadTx(ctx, tx, existing)
		if err != nil {
			return domain.LeadRecord{}, false, err
		}
		return lead, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.LeadRecord{}, false, err
	}
	item, err := s.itemTx(ctx, tx, in.SellerID, in.ItemID)
	if err != nil {
		return domain.LeadRecord{}, false, err
	}
	if !item.Active {
		return domain.LeadRecord{}, false, commerceProblem(contracts.InvalidState, "item is inactive")
	}
	if item.AvailableStock < 1 {
		return domain.LeadRecord{}, false, commerceProblem(contracts.OutOfStock, "item is no longer available")
	}
	// Alternatives stay within seller scope; their stock is rechecked only if a
	// later offer selects one. Deduplicate IDs without altering caller input.
	alternatives := make([]string, 0, len(in.AlternativeItemIDs))
	seen := map[string]bool{in.ItemID: true}
	for _, id := range in.AlternativeItemIDs {
		if !validIdentity(id) {
			return domain.LeadRecord{}, false, commerceProblem(contracts.InvalidArgument, "invalid alternative item")
		}
		if seen[id] {
			continue
		}
		if _, err = s.itemTx(ctx, tx, in.SellerID, id); err != nil {
			return domain.LeadRecord{}, false, err
		}
		seen[id] = true
		alternatives = append(alternatives, id)
	}
	intent, err := json.Marshal(in.Intent)
	if err != nil {
		return domain.LeadRecord{}, false, err
	}
	alternativeJSON, err := json.Marshal(alternatives)
	if err != nil {
		return domain.LeadRecord{}, false, err
	}
	id := s.id()
	now := s.now().UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO leads(id,seller_id,platform,post_id,buyer_id,thread_id,item_id,state,intent_json,match_reason,alternative_item_ids_json,state_reason,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'active',?,?,?,'',1,?,?)`, id, in.SellerID, in.Platform, in.PostID, in.BuyerID, in.ThreadID, in.ItemID, string(intent), strings.TrimSpace(in.MatchReason), string(alternativeJSON), now, now)
	if err != nil {
		return domain.LeadRecord{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_notifications(event_id,lead_id,status,attempts,next_attempt_at,accepted_at,error_code,error_message,created_at,updated_at) VALUES(?,?,'pending',0,NULL,NULL,'','',?,?)`, s.id(), id, now, now)
	if err != nil {
		return domain.LeadRecord{}, false, err
	}
	lead, err := s.leadTx(ctx, tx, id)
	if err != nil {
		return domain.LeadRecord{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return domain.LeadRecord{}, false, err
	}
	return lead, true, nil
}

// TransitionLead is the agent-facing transition boundary. Only active leads
// can escalate or close. A retry to the same target and original reason is a
// no-op; a changed reason is a conflict, so terminal history is not rewritten.
// Seller hand-back, payment, expiry and cancellation are separate operations.
func (s *Service) TransitionLead(ctx context.Context, leadID string, target domain.LeadState, reason string) (domain.LeadRecord, error) {
	reason = strings.TrimSpace(reason)
	if !validIdentity(leadID) || (target != domain.LeadEscalated && target != domain.LeadLost) || reason == "" || len(reason) > 2000 {
		return domain.LeadRecord{}, commerceProblem(contracts.InvalidArgument, "invalid agent lead transition")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return domain.LeadRecord{}, err
	}
	defer tx.Rollback()
	lead, err := s.leadTx(ctx, tx, leadID)
	if err != nil {
		return domain.LeadRecord{}, err
	}
	if lead.State == target {
		var original string
		if err = tx.QueryRowContext(ctx, `SELECT state_reason FROM leads WHERE id=?`, leadID).Scan(&original); err != nil {
			return domain.LeadRecord{}, err
		}
		if original != reason {
			return domain.LeadRecord{}, commerceProblem(contracts.InvalidState, "lead was already transitioned with a different reason")
		}
		return lead, tx.Commit()
	}
	if lead.State != domain.LeadActive {
		return domain.LeadRecord{}, commerceProblem(contracts.InvalidState, "lead is not active")
	}
	_, err = tx.ExecContext(ctx, `UPDATE leads SET state=?,state_reason=?,version=version+1,updated_at=? WHERE id=? AND state='active'`, target, reason, s.now().UnixMilli(), leadID)
	if err != nil {
		return domain.LeadRecord{}, err
	}
	lead, err = s.leadTx(ctx, tx, leadID)
	if err != nil {
		return domain.LeadRecord{}, err
	}
	return lead, tx.Commit()
}
