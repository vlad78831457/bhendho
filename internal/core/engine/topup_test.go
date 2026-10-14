package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// Метка «Пополнить» (ADR-62): служба оплаты гасит код по метке, без email.
func TestTopupMark(t *testing.T) {
	s := setup(t)
	shop := s.issuer("shop", shopToken)
	other := s.issuer("other-shop", "other-token-other-token-other-token-01")
	alice := s.user("")

	m, err := s.e.IssueTopupMark(s.ctx, alice)
	must(t, err)
	if !strings.HasPrefix(m.Mark, "tm_") || !m.ExpiresAt.After(time.Now()) {
		t.Fatalf("mark: %+v", m)
	}
	var stored int
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT count(*) FROM topup_marks WHERE mark_hash = $1`, m.Mark).Scan(&stored))
	if stored != 0 {
		t.Fatal("the mark itself must not be stored")
	}

	if _, err := s.e.RedeemPromoByMark(s.ctx, shop, "tm_nope", "ANY"); !errors.Is(err, ErrMarkInvalid) {
		t.Fatalf("unknown mark: %v", err)
	}

	// Код чужого эмитента по метке не гасится, и метка остаётся свободной.
	foreign := s.promo(other, PromoSpec{Amount: decimal.RequireFromString("50")})
	if _, err := s.e.RedeemPromoByMark(s.ctx, shop, m.Mark, foreign.Code); !errors.Is(err, ErrPromoInvalid) {
		t.Fatalf("foreign issuer: %v", err)
	}

	p := s.promo(shop, PromoSpec{Amount: decimal.RequireFromString("500")})
	res, err := s.e.RedeemPromoByMark(s.ctx, shop, m.Mark, p.Code)
	must(t, err)
	if res.AlreadyRedeemed || s.balance(alice) != "500.0000" {
		t.Fatalf("redeem by mark: %+v, balance %s", res, s.balance(alice))
	}
	// Повтор вебхука — тот же код, денег не прибавилось.
	again, err := s.e.RedeemPromoByMark(s.ctx, shop, m.Mark, p.Code)
	must(t, err)
	if !again.AlreadyRedeemed || s.balance(alice) != "500.0000" {
		t.Fatalf("webhook retry: %+v, balance %s", again, s.balance(alice))
	}
	// Второй код по той же метке откатывается целиком: код остаётся непогашенным.
	p2 := s.promo(shop, PromoSpec{Amount: decimal.RequireFromString("100")})
	if _, err := s.e.RedeemPromoByMark(s.ctx, shop, m.Mark, p2.Code); !errors.Is(err, ErrMarkUsed) {
		t.Fatalf("second code by the same mark: %v", err)
	}
	if s.balance(alice) != "500.0000" {
		t.Fatalf("second code credited: %s", s.balance(alice))
	}
	var status string
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT status FROM promo_codes WHERE id = $1`, p2.ID).Scan(&status))
	if status != "active" {
		t.Fatalf("second code status %q", status)
	}

	// Просроченная неиспользованная метка не принимается; использованная — принимает повтор и после срока.
	stale, err := s.e.IssueTopupMark(s.ctx, alice)
	must(t, err)
	s.e.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	if _, err := s.e.RedeemPromoByMark(s.ctx, shop, stale.Mark, p2.Code); !errors.Is(err, ErrMarkExpired) {
		t.Fatalf("expired mark: %v", err)
	}
	if late, err := s.e.RedeemPromoByMark(s.ctx, shop, m.Mark, p.Code); err != nil || !late.AlreadyRedeemed {
		t.Fatalf("retry after expiry: %+v %v", late, err)
	}
}
