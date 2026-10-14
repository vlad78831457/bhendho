package engine

import (
	"encoding/json"
	"strconv"
	"testing"

	"offgrid/core/internal/core/codes"
	"offgrid/core/internal/core/config"
)

// commitImages — прораб берёт задачу и отчитывается картинками (1 картинка = 20 GC в тестовом паспорте).
func (s *env) commitImages(images int) {
	s.t.Helper()
	c := s.claimOne()
	if c == nil {
		s.t.Fatal("nothing to claim")
	}
	_, err := s.e.Accept(s.ctx, s.acc, c.TaskID, true, 10)
	must(s.t, err)
	var report map[string]any
	must(s.t, json.Unmarshal([]byte(`{"worker_charge": {"lines": [{"unit": "images", "units": `+strconv.Itoa(images)+`}]}}`), &report))
	_, err = s.e.Commit(s.ctx, s.acc, c.TaskID, CommitRequest{Outcome: "success", Result: map[string]any{"pages": 1}, Report: report})
	must(s.t, err)
}

// ADR-63: списание сверх резерва — до резерв × множитель; кошелёк уходит в минус; в минусе задач не берём.
func TestOverdraftUpToFactor(t *testing.T) {
	s := setup(t)
	u := s.user("160")
	hero := s.hero(u, true)

	_, err := s.create(u, hero, "k1")
	must(t, err)
	s.commitImages(9) // 180 GC при резерве 150: 150 из резерва, 30 сверх
	if bal, res := s.wallet(u); bal != "-20.0000" || res != "0.0000" {
		t.Fatalf("after overdraft: balance %s reserved %s", bal, res)
	}
	var charge, over string
	must(t, s.e.pool.QueryRow(s.ctx, `
		SELECT COALESCE(sum(amount) FILTER (WHERE type = 'charge'), 0)::text, COALESCE(sum(amount) FILTER (WHERE type = 'overcharge'), 0)::text
		FROM transactions`).Scan(&charge, &over))
	if charge != "150.0000" || over != "30.0000" {
		t.Fatalf("journal: charge %s overcharge %s", charge, over)
	}
	s.checkMoney()

	// В минусе новая задача не создаётся.
	if _, err := s.create(u, hero, "k2"); refusalCode(err) != codes.InsufficientFunds {
		t.Fatalf("task while in the red: %v", err)
	}
}

func TestOverdraftCapped(t *testing.T) {
	s := setup(t)
	u := s.user("1000")
	_, err := s.create(u, s.hero(u, true), "k1")
	must(t, err)
	s.commitImages(100) // 2000 GC — упирается в 150 × 1.5 = 225
	if bal, _ := s.wallet(u); bal != "775.0000" {
		t.Fatalf("capped charge: balance %s", bal)
	}
	s.checkMoney()
}

func TestOverdraftFactorOne(t *testing.T) {
	s := setup(t, func(c *config.Config) { c.OverdraftFactor = "1" })
	u := s.user("1000")
	_, err := s.create(u, s.hero(u, true), "k1")
	must(t, err)
	s.commitImages(100)
	if bal, _ := s.wallet(u); bal != "850.0000" {
		t.Fatalf("factor 1 keeps the old cap: balance %s", bal)
	}
	s.checkMoney()
}
