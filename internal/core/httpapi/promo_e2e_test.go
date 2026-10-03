package httpapi

import (
	"net/http"
	"testing"
)

// Промокоды через HTTP (ADR-57): служба оплаты выпускает и гасит по вебхуку, пользователь вводит код.
func TestPromoCodesOverHTTP(t *testing.T) {
	s := newStack(t)
	defer s.cleanup()
	user := s.register("promo@example.org")

	// Без токена эмитента выпуск закрыт; токен пользователя — не токен эмитента.
	s.call("POST", "/issuer/v1/promo-codes", "", map[string]any{"amount": 500}, http.StatusUnauthorized)
	s.call("POST", "/issuer/v1/promo-codes", user, map[string]any{"amount": 500}, http.StatusUnauthorized)

	issued := s.call("POST", "/issuer/v1/promo-codes", s.issuer, map[string]any{"amount": "500", "external_id": "order-1"}, http.StatusCreated)
	code := issued["code"].(string)
	id := issued["code_id"].(string)
	repeat := s.call("POST", "/issuer/v1/promo-codes", s.issuer, map[string]any{"amount": "500", "external_id": "order-1"}, http.StatusOK)
	if repeat["code_id"] != id || repeat["code"] != nil {
		t.Fatalf("retry of the same order: %v", repeat)
	}
	s.call("POST", "/issuer/v1/promo-codes", s.issuer, map[string]any{"amount": "0"}, http.StatusUnprocessableEntity)

	s.call("POST", "/api/v1/promo-codes/redeem", "", map[string]any{"code": code}, http.StatusUnauthorized)
	s.call("POST", "/api/v1/promo-codes/redeem", user, map[string]any{"code": "WRONG-WRONG-WRONG-XX"}, http.StatusNotFound)
	res := s.call("POST", "/api/v1/promo-codes/redeem", user, map[string]any{"code": code}, http.StatusOK)
	if res["amount"] != "500.0000" || res["wallet"].(map[string]any)["available"] != "500.0000" {
		t.Fatalf("redeem: %v", res)
	}
	other := s.register("other@example.org")
	s.call("POST", "/api/v1/promo-codes/redeem", other, map[string]any{"code": code}, http.StatusConflict)

	st := s.call("GET", "/issuer/v1/promo-codes/"+id, s.issuer, nil, http.StatusOK)
	if st["status"] != "redeemed" {
		t.Fatalf("status: %v", st)
	}
	s.call("POST", "/issuer/v1/promo-codes/"+id+"/revoke", s.issuer, nil, http.StatusConflict)

	// Вебхук: «оплачено — зачислите other@example.org».
	gift := s.call("POST", "/issuer/v1/promo-codes", s.issuer, map[string]any{"amount": "120", "for_email": "other@example.org"}, http.StatusCreated)
	s.call("POST", "/issuer/v1/redeem", s.issuer, map[string]any{"code": gift["code"], "user_email": "nobody@example.org"}, http.StatusNotFound)
	done := s.call("POST", "/issuer/v1/redeem", s.issuer, map[string]any{"code": gift["code"], "user_email": "OTHER@example.org"}, http.StatusOK)
	if done["wallet"].(map[string]any)["balance"] != "120.0000" {
		t.Fatalf("webhook: %v", done)
	}

	// Подбор: после 10 неверных кодов — 429 (чистый пользователь: у other уже есть неудачная попытка).
	guesser := s.register("guesser@example.org")
	for range 10 {
		s.call("POST", "/api/v1/promo-codes/redeem", guesser, map[string]any{"code": "GUESS-GUESS-GUESS-GUESS"}, http.StatusNotFound)
	}
	s.call("POST", "/api/v1/promo-codes/redeem", guesser, map[string]any{"code": "GUESS-GUESS-GUESS-GUESS"}, http.StatusTooManyRequests)
}
