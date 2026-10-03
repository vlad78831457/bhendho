package engine

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const shopToken = "shop-token-shop-token-shop-token-0001"

func (s *env) issuer(name, token string) PromoIssuer {
	s.t.Helper()
	must(s.t, s.e.EnsurePromoIssuer(s.ctx, name, token))
	iss, err := s.e.AuthenticatePromoIssuer(s.ctx, token)
	must(s.t, err)
	return iss
}

func (s *env) promo(iss PromoIssuer, spec PromoSpec) PromoCode {
	s.t.Helper()
	p, created, err := s.e.IssuePromo(s.ctx, iss, spec)
	must(s.t, err)
	if !created || p.Code == "" {
		s.t.Fatalf("expected a new code, got %+v created=%v", p, created)
	}
	return p
}

func (s *env) balance(user uuid.UUID) string {
	b, _ := s.wallet(user)
	return b
}

func (s *env) email(user uuid.UUID) string {
	return user.String() + "@test"
}

func TestPromoRedeemedExactlyOnce(t *testing.T) {
	s := setup(t)
	shop := s.issuer("shop", shopToken)
	p := s.promo(shop, PromoSpec{Amount: decimal.RequireFromString("500")})
	if len(p.Code) != 19 || strings.Count(p.Code, "-") != 3 {
		t.Fatalf("code format: %q", p.Code)
	}

	alice, bob := s.user(""), s.user("")
	// Регистр, пробелы и дефисы не важны — код переписывают руками.
	typed := strings.ToLower(strings.ReplaceAll(p.Code, "-", " "))
	res, err := s.e.RedeemPromo(s.ctx, alice, typed)
	must(t, err)
	if res.AlreadyRedeemed || res.Amount != "500.0000" || res.Wallet.Balance != "500.0000" {
		t.Fatalf("first redemption: %+v", res)
	}
	var provider string
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT provider FROM deposits WHERE user_id = $1`, alice).Scan(&provider))
	if provider != "promo:shop" {
		t.Fatalf("deposit provider %q", provider)
	}

	// Повтор того же пользователя (двойной клик) — ответ тот же, денег не прибавилось.
	again, err := s.e.RedeemPromo(s.ctx, alice, p.Code)
	must(t, err)
	if !again.AlreadyRedeemed || s.balance(alice) != "500.0000" {
		t.Fatalf("repeat by the same user: %+v, balance %s", again, s.balance(alice))
	}
	if _, err := s.e.RedeemPromo(s.ctx, bob, p.Code); !errors.Is(err, ErrPromoUsed) {
		t.Fatalf("second user: %v", err)
	}
	if _, err := s.e.RedeemPromo(s.ctx, bob, "NOPE-NOPE-NOPE-NOPE"); !errors.Is(err, ErrPromoInvalid) {
		t.Fatalf("unknown code: %v", err)
	}
	st, err := s.e.PromoStatus(s.ctx, shop, p.ID)
	must(t, err)
	if st.Status != "redeemed" || st.RedeemedAt == nil || st.Code != "" {
		t.Fatalf("status for the issuer: %+v", st)
	}
	s.checkMoney()
}

func TestPromoConcurrentRedemptionPaysOnce(t *testing.T) {
	s := setup(t)
	p := s.promo(s.issuer("shop", shopToken), PromoSpec{Amount: decimal.RequireFromString("300")})
	users := make([]uuid.UUID, 10)
	for i := range users {
		users[i] = s.user("")
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		ok, used int
	)
	for _, u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.e.RedeemPromo(s.ctx, u, p.Code)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrPromoUsed):
				used++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	var total string
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT COALESCE(sum(amount), 0)::text FROM transactions WHERE type = 'deposit'`).Scan(&total))
	if ok != 1 || used != 9 || !decimal.RequireFromString(total).Equal(decimal.NewFromInt(300)) {
		t.Fatalf("ok=%d used=%d deposited=%s", ok, used, total)
	}
	s.checkMoney()
}

func TestPromoPersonalExpiredRevoked(t *testing.T) {
	s := setup(t)
	shop := s.issuer("shop", shopToken)
	alice, bob := s.user(""), s.user("")

	// Персональный код: чужому пользователю — «нет такого», хозяину — зачисление.
	personal := s.promo(shop, PromoSpec{Amount: decimal.NewFromInt(100), ForEmail: strings.ToUpper(s.email(alice))})
	if _, err := s.e.RedeemPromo(s.ctx, bob, personal.Code); !errors.Is(err, ErrPromoInvalid) {
		t.Fatalf("someone else's personal code: %v", err)
	}
	_, err := s.e.RedeemPromo(s.ctx, alice, personal.Code)
	must(t, err)

	soon := time.Now().Add(time.Hour)
	expired := s.promo(shop, PromoSpec{Amount: decimal.NewFromInt(100), ExpiresAt: &soon})
	_, err = s.e.pool.Exec(s.ctx, `UPDATE promo_codes SET expires_at = now() - interval '1 minute' WHERE id = $1`, expired.ID)
	must(t, err)
	if _, err := s.e.RedeemPromo(s.ctx, bob, expired.Code); !errors.Is(err, ErrPromoExpired) {
		t.Fatalf("expired: %v", err)
	}

	revoked := s.promo(shop, PromoSpec{Amount: decimal.NewFromInt(100)})
	st, err := s.e.RevokePromo(s.ctx, shop, revoked.ID)
	must(t, err)
	if st.Status != "revoked" {
		t.Fatalf("revoke: %+v", st)
	}
	if _, err := s.e.RedeemPromo(s.ctx, bob, revoked.Code); !errors.Is(err, ErrPromoInvalid) {
		t.Fatalf("revoked: %v", err)
	}
	if _, err := s.e.RevokePromo(s.ctx, shop, personal.ID); !errors.Is(err, ErrPromoNotRevocable) {
		t.Fatalf("revoking a redeemed code: %v", err)
	}
	if s.balance(bob) != "0.0000" || s.balance(alice) != "100.0000" {
		t.Fatalf("balances: bob %s, alice %s", s.balance(bob), s.balance(alice))
	}

	for _, bad := range []string{"0", "-5", "100000.01", "1.00001"} {
		if _, _, err := s.e.IssuePromo(s.ctx, shop, PromoSpec{Amount: decimal.RequireFromString(bad)}); refusalCode(err) == "" {
			t.Fatalf("amount %s must be refused, got %v", bad, err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if _, _, err := s.e.IssuePromo(s.ctx, shop, PromoSpec{Amount: decimal.NewFromInt(1), ExpiresAt: &past}); refusalCode(err) == "" {
		t.Fatalf("expiry in the past must be refused, got %v", err)
	}
	s.checkMoney()
}

func TestPromoIssueIsIdempotentPerOrder(t *testing.T) {
	s := setup(t)
	shop := s.issuer("shop", shopToken)
	other := s.issuer("partner", "partner-token-partner-token-000001")

	first := s.promo(shop, PromoSpec{Amount: decimal.NewFromInt(250), ExternalID: "order-42"})
	again, created, err := s.e.IssuePromo(s.ctx, shop, PromoSpec{Amount: decimal.NewFromInt(250), ExternalID: "order-42"})
	must(t, err)
	if created || again.ID != first.ID || again.Code != "" {
		t.Fatalf("retry of the same order must return the issued code without its text: %+v", again)
	}
	if _, _, err := s.e.IssuePromo(s.ctx, shop, PromoSpec{Amount: decimal.NewFromInt(999), ExternalID: "order-42"}); !errors.Is(err, ErrPromoConflict) {
		t.Fatalf("same order, other amount: %v", err)
	}

	// Свой код эмитента: повтор возвращает его текст; такой же код у другого эмитента — конфликт.
	own := "SUMMER-2026-GIFT-777"
	p := s.promo(shop, PromoSpec{Amount: decimal.NewFromInt(50), Code: own, ExternalID: "order-43"})
	if p.Code != own {
		t.Fatalf("own code: %+v", p)
	}
	rep, created, err := s.e.IssuePromo(s.ctx, shop, PromoSpec{Amount: decimal.NewFromInt(50), Code: own, ExternalID: "order-43"})
	must(t, err)
	if created || rep.Code != own {
		t.Fatalf("retry with own code: %+v", rep)
	}
	if _, _, err := s.e.IssuePromo(s.ctx, other, PromoSpec{Amount: decimal.NewFromInt(50), Code: "summer 2026 gift 777"}); !errors.Is(err, ErrPromoConflict) {
		t.Fatalf("same code at another issuer: %v", err)
	}
	if _, _, err := s.e.IssuePromo(s.ctx, shop, PromoSpec{Amount: decimal.NewFromInt(50), Code: "SHORT"}); refusalCode(err) == "" {
		t.Fatalf("too short own code must be refused, got %v", err)
	}
}

func TestPromoWebhookAndIssuerBoundaries(t *testing.T) {
	s := setup(t)
	shop := s.issuer("shop", shopToken)
	partner := s.issuer("partner", "partner-token-partner-token-000001")
	alice := s.user("")
	p := s.promo(shop, PromoSpec{Amount: decimal.NewFromInt(700), ExternalID: "order-7"})

	// Эмитент гасит только свои коды.
	if _, err := s.e.RedeemPromoFor(s.ctx, partner, s.email(alice), p.Code); !errors.Is(err, ErrPromoInvalid) {
		t.Fatalf("foreign issuer: %v", err)
	}
	if _, err := s.e.RedeemPromoFor(s.ctx, shop, "nobody@test", p.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown email: %v", err)
	}
	res, err := s.e.RedeemPromoFor(s.ctx, shop, strings.ToUpper(s.email(alice)), p.Code)
	must(t, err)
	if res.Wallet.Balance != "700.0000" {
		t.Fatalf("webhook redemption: %+v", res)
	}
	// Повтор вебхука — без второго зачисления.
	rep, err := s.e.RedeemPromoFor(s.ctx, shop, s.email(alice), p.Code)
	must(t, err)
	if !rep.AlreadyRedeemed || s.balance(alice) != "700.0000" {
		t.Fatalf("webhook retry: %+v", rep)
	}
	if _, err := s.e.PromoStatus(s.ctx, partner, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("status of a foreign code: %v", err)
	}
	if _, err := s.e.AuthenticatePromoIssuer(s.ctx, "wrong-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong token: %v", err)
	}
	s.checkMoney()
}

func TestPromoGuessingIsLimited(t *testing.T) {
	s := setup(t)
	p := s.promo(s.issuer("shop", shopToken), PromoSpec{Amount: decimal.NewFromInt(10)})
	u := s.user("")
	for range s.e.cfg.PromoMaxFailures {
		if _, err := s.e.RedeemPromo(s.ctx, u, "GUESS-GUESS-GUESS-GUESS"); !errors.Is(err, ErrPromoInvalid) {
			t.Fatalf("wrong guess: %v", err)
		}
	}
	// Даже верный код не принимается, пока не прошло окно.
	if _, err := s.e.RedeemPromo(s.ctx, u, p.Code); !errors.Is(err, ErrPromoTooMany) {
		t.Fatalf("after %d wrong codes: %v", s.e.cfg.PromoMaxFailures, err)
	}
	// Другого пользователя лимит не касается.
	if _, err := s.e.RedeemPromo(s.ctx, s.user(""), p.Code); err != nil {
		t.Fatal(err)
	}
}
