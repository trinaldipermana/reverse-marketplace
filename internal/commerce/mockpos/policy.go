package mockpos

import (
	"context"
	"database/sql"
	"errors"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func policy(ctx context.Context, tx *sql.Tx, seller string) (Policy, error) {
	var p Policy
	err := tx.QueryRowContext(ctx, `SELECT floor_price_idr,max_discount_bps,message_cap,rate_limit_per_minute,version FROM seller_policies WHERE seller_id=?`, seller).Scan(&p.FloorPriceIDR, &p.MaxDiscountBPS, &p.MessageCap, &p.RateLimitPerMinute, &p.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return p, problem(contracts.NotFound, "seller policy not found")
	}
	return p, err
}
func (s *Service) GetPolicy(ctx context.Context, seller string) (Policy, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Policy{}, err
	}
	defer tx.Rollback()
	p, err := policy(ctx, tx, seller)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}
func (s *Service) GetRules(ctx context.Context, seller, item string) (domain.Rules, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Rules{}, err
	}
	defer tx.Rollback()
	if _, err = s.item(ctx, tx, seller, item); err != nil {
		return domain.Rules{}, err
	}
	p, err := policy(ctx, tx, seller)
	if err != nil {
		return domain.Rules{}, err
	}
	rules := domain.Rules{FloorPriceIDR: p.FloorPriceIDR, MaxDiscountBPS: p.MaxDiscountBPS, MessageCap: p.MessageCap, RateLimitPerMinute: p.RateLimitPerMinute, Version: p.Version}
	return rules, tx.Commit()
}
func (s *Service) PatchPolicy(ctx context.Context, seller, key string, in PatchPolicy) (Policy, error) {
	if in.ExpectedVersion < 1 || in.FloorPriceIDR == nil && in.MaxDiscountBPS == nil && in.MessageCap == nil && in.RateLimitPerMinute == nil {
		return Policy{}, problem(contracts.InvalidArgument, "expected_version and policy fields are required")
	}
	return mutate(ctx, s, seller, key, "patch_policy", in, func(tx *sql.Tx) (Policy, error) {
		p, err := policy(ctx, tx, seller)
		if err != nil {
			return p, err
		}
		if p.Version != in.ExpectedVersion {
			return Policy{}, problem(contracts.VersionConflict, "policy changed; reload before editing")
		}
		if in.FloorPriceIDR != nil {
			p.FloorPriceIDR = *in.FloorPriceIDR
		}
		if in.MaxDiscountBPS != nil {
			p.MaxDiscountBPS = *in.MaxDiscountBPS
		}
		if in.MessageCap != nil {
			p.MessageCap = *in.MessageCap
		}
		if in.RateLimitPerMinute != nil {
			p.RateLimitPerMinute = *in.RateLimitPerMinute
		}
		if p.FloorPriceIDR < 0 || p.FloorPriceIDR > maxPrice || p.MaxDiscountBPS < 0 || p.MaxDiscountBPS > 10000 || p.MessageCap < 1 || p.MessageCap > 1000 || p.RateLimitPerMinute < 1 || p.RateLimitPerMinute > 10000 {
			return Policy{}, problem(contracts.InvalidArgument, "invalid seller policy")
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM inventory_items WHERE seller_id=? AND active=1 AND list_price_idr<?`, seller, p.FloorPriceIDR).Scan(&count); err != nil {
			return Policy{}, err
		}
		if count > 0 {
			return Policy{}, problem(contracts.InvalidArgument, "policy conflicts with active inventory prices")
		}
		_, err = tx.ExecContext(ctx, `UPDATE seller_policies SET floor_price_idr=?,max_discount_bps=?,message_cap=?,rate_limit_per_minute=?,version=version+1,updated_at=? WHERE seller_id=? AND version=?`, p.FloorPriceIDR, p.MaxDiscountBPS, p.MessageCap, p.RateLimitPerMinute, s.now().UnixMilli(), seller, in.ExpectedVersion)
		if err != nil {
			return Policy{}, err
		}
		return policy(ctx, tx, seller)
	})
}
