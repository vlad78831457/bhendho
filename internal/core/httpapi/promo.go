package httpapi

// Промокоды (ADR-57): пользователь вводит код; служба оплаты (эмитент, свой токен) выпускает коды,
// смотрит их статус, отзывает и сообщает «код оплачен — зачислите пользователю с этим email».

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/engine"
)

type issuerHandler func(w http.ResponseWriter, r *http.Request, iss engine.PromoIssuer)

// issuer — токен эмитента промокодов в Authorization: Bearer (как у прорабов, но свой).
func (s *Server) issuer(h issuerHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		iss, err := s.eng.AuthenticatePromoIssuer(r.Context(), bearer(r))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid promo issuer token required")
			return
		}
		h(w, r, iss)
	}
}

// promoFail — отказы промокодов: у каждого свой HTTP-статус и код, чтобы экран показал понятный текст.
func (s *Server) promoFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrPromoInvalid):
		writeError(w, http.StatusNotFound, "promo_invalid", "promo code is not valid")
	case errors.Is(err, engine.ErrPromoUsed):
		writeError(w, http.StatusConflict, "promo_used", "promo code already redeemed")
	case errors.Is(err, engine.ErrPromoExpired):
		writeError(w, http.StatusGone, "promo_expired", "promo code expired")
	case errors.Is(err, engine.ErrPromoTooMany):
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "too many wrong promo codes, try later")
	case errors.Is(err, engine.ErrPromoConflict):
		writeError(w, http.StatusConflict, "promo_conflict", "this code or external_id is already issued with other data")
	case errors.Is(err, engine.ErrPromoNotRevocable):
		writeError(w, http.StatusConflict, "promo_not_revocable", "only an active promo code can be revoked")
	default:
		s.fail(w, err)
	}
}

func (s *Server) redeemPromo(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	res, err := s.eng.RedeemPromo(r.Context(), u, req.Code)
	if err != nil {
		s.promoFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) issuePromo(w http.ResponseWriter, r *http.Request, iss engine.PromoIssuer) {
	var req struct {
		Amount     decimal.Decimal `json:"amount"`
		Code       string          `json:"code"`
		ExternalID string          `json:"external_id"`
		ForEmail   string          `json:"for_email"`
		ExpiresAt  *time.Time      `json:"expires_at"`
	}
	if !decode(w, r, &req) {
		return
	}
	p, created, err := s.eng.IssuePromo(r.Context(), iss, engine.PromoSpec{
		Amount: req.Amount, Code: req.Code, ExternalID: req.ExternalID, ForEmail: req.ForEmail, ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		s.promoFail(w, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK // повтор заказа с тем же external_id — код уже выпущен
	}
	writeJSON(w, status, p)
}

func (s *Server) promoStatus(w http.ResponseWriter, r *http.Request, iss engine.PromoIssuer) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := s.eng.PromoStatus(r.Context(), iss, id)
	if err != nil {
		s.promoFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) revokePromo(w http.ResponseWriter, r *http.Request, iss engine.PromoIssuer) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := s.eng.RevokePromo(r.Context(), iss, id)
	if err != nil {
		s.promoFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// issuerRedeem — вебхук службы оплаты: оплату получили, зачислите код пользователю.
func (s *Server) issuerRedeem(w http.ResponseWriter, r *http.Request, iss engine.PromoIssuer) {
	var req struct {
		Code      string `json:"code"`
		UserEmail string `json:"user_email"`
	}
	if !decode(w, r, &req) {
		return
	}
	res, err := s.eng.RedeemPromoFor(r.Context(), iss, req.UserEmail, req.Code)
	if err != nil {
		s.promoFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
