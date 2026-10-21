package engine

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/codes"
)

func dec(s string) *decimal.Decimal { d := decimal.RequireFromString(s); return &d }

func (s *env) reserved(id uuid.UUID) string {
	s.t.Helper()
	var r string
	must(s.t, s.e.pool.QueryRow(s.ctx, `SELECT reserved::text FROM system_tasks WHERE id = $1`, id).Scan(&r))
	return r
}

// ADR-69: тарифы меняются без перезапуска; задача помнит цену, курсы и множитель, действовавшие при создании.
func TestTariffsApplyToNewTasksOnly(t *testing.T) {
	s := setup(t)
	admin := uuid.New()
	u := s.user("1000")
	hero := s.hero(u, true)

	before, err := s.create(u, hero, "before")
	must(t, err)
	must(t, s.e.SetTariff(s.ctx, admin, "form_price", "tale.start.v1", dec("200")))
	must(t, s.e.SetTariff(s.ctx, admin, "unit_rate", "tale:images", dec("10000"))) // картинка: 20 → 10 GC
	must(t, s.e.SetTariff(s.ctx, admin, "overdraft_factor", "", dec("1")))
	after, err := s.create(u, hero, "after")
	must(t, err)

	if s.reserved(taskID(before)) != "150.0000" || s.reserved(taskID(after)) != "200.0000" {
		t.Fatalf("reserves: before %s after %s", s.reserved(taskID(before)), s.reserved(taskID(after)))
	}
	items, err := s.e.Catalog(s.ctx)
	must(t, err)
	for _, it := range items {
		if it.FormID == "tale.start.v1" && it.Price != "200.0000" {
			t.Fatalf("catalog price %s", it.Price)
		}
	}

	// Старая задача: картинка по 20 GC, крышка 1.5 × 150 = 225 → 10 картинок = 200 GC, списано всё.
	s.commitImages(10)
	// Новая: картинка по 10 GC, крышка 1 × 200 → 30 картинок = 300 GC упираются в 200.
	s.commitImages(30)
	var charges []string
	rows, err := s.e.pool.Query(s.ctx, `SELECT sum(amount)::text FROM transactions WHERE type IN ('charge', 'overcharge')
		GROUP BY task_id ORDER BY min(id)`)
	must(t, err)
	for rows.Next() {
		var c string
		must(t, rows.Scan(&c))
		charges = append(charges, c)
	}
	rows.Close()
	if len(charges) != 2 || charges[0] != "200.0000" || charges[1] != "200.0000" {
		t.Fatalf("charges: %v", charges)
	}
	s.checkMoney()

	// Сброс — снова как в паспорте.
	must(t, s.e.SetTariff(s.ctx, admin, "form_price", "tale.start.v1", nil))
	reset, err := s.create(u, hero, "reset")
	must(t, err)
	if s.reserved(taskID(reset)) != "150.0000" {
		t.Fatalf("after reset: %s", s.reserved(taskID(reset)))
	}

	tr, err := s.e.AdminTariffs(s.ctx)
	must(t, err)
	if len(tr.History) != 4 || tr.Overdraft.Value != "1" || !tr.Overdraft.Override {
		t.Fatalf("tariffs: history %d overdraft %+v", len(tr.History), tr.Overdraft)
	}
	var images TariffUnit
	for _, u := range tr.Units {
		if u.Unit == "images" {
			images = u
		}
	}
	if images.Rate != "10000" || images.Manifest != "20000" || !images.Override {
		t.Fatalf("images rate: %+v", images)
	}
}

func TestTariffValidation(t *testing.T) {
	s := setup(t)
	admin := uuid.New()
	for name, c := range map[string]struct {
		scope, target string
		value         *decimal.Decimal
	}{
		"unknown form":       {"form_price", "nope.v1", dec("10")},
		"fact form":          {"form_price", "tale.character.v1", dec("10")},
		"unknown unit":       {"unit_rate", "tale:gpu", dec("1")},
		"no department":      {"unit_rate", "images", dec("1")},
		"factor below one":   {"overdraft_factor", "", dec("0.5")},
		"zero price":         {"form_price", "tale.start.v1", dec("0")},
		"unknown scope":      {"discount", "", dec("1")},
		"factor with target": {"overdraft_factor", "x", dec("2")},
	} {
		if err := s.e.SetTariff(s.ctx, admin, c.scope, c.target, c.value); refusalCode(err) != codes.ValidationError {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Журнал только дописывается.
	must(t, s.e.SetTariff(s.ctx, admin, "overdraft_factor", "", dec("2")))
	if _, err := s.e.pool.Exec(s.ctx, `UPDATE tariff_changes SET value = 3`); err == nil {
		t.Fatal("tariff journal must be append-only")
	}
}

// ADR-78: коэффициенты ядра × подразделения — в резерве и в факте; прораба — при выдаче задачи, в факте.
func TestMultipliers(t *testing.T) {
	s := setup(t)
	admin := uuid.New()
	u := s.user("1000")
	hero := s.hero(u, true)

	plain, err := s.create(u, hero, "plain")
	must(t, err)
	if s.reserved(taskID(plain)) != "150.0000" {
		t.Fatalf("default reserve %s", s.reserved(taskID(plain)))
	}
	s.commitImages(1) // 1 картинка = 20 GC, все k = 1

	must(t, s.e.SetTariff(s.ctx, admin, "multiplier", "core", dec("1.5")))
	must(t, s.e.SetTariff(s.ctx, admin, "multiplier", "tale", dec("2")))
	must(t, s.e.SetTariff(s.ctx, admin, "multiplier", "tale:prorab-1", dec("0.5")))
	priced, err := s.create(u, hero, "priced")
	must(t, err)
	if s.reserved(taskID(priced)) != "450.0000" {
		t.Fatalf("reserve × k: %s", s.reserved(taskID(priced)))
	}
	s.commitImages(1) // 20 × 1.5 × 2 × 0.5 = 30 GC

	var charges []string
	rows, err := s.e.pool.Query(s.ctx, `SELECT sum(amount)::text FROM transactions WHERE type IN ('charge', 'overcharge')
		GROUP BY task_id ORDER BY min(id)`)
	must(t, err)
	for rows.Next() {
		var c string
		must(t, rows.Scan(&c))
		charges = append(charges, c)
	}
	rows.Close()
	if len(charges) != 2 || charges[0] != "20.0000" || charges[1] != "30.0000" {
		t.Fatalf("charges: %v", charges)
	}
	var rates string
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT rates::text FROM transactions WHERE type = 'charge' ORDER BY id DESC LIMIT 1`).Scan(&rates))
	for _, want := range []string{`"multiplier": "3"`, `"prorab": "prorab-1"`, `"prorab_multiplier": "0.5"`} {
		if !strings.Contains(rates, want) {
			t.Fatalf("charge rates %s: no %s", rates, want)
		}
	}
	s.checkMoney()

	tr, err := s.e.AdminTariffs(s.ctx)
	must(t, err)
	got := map[string]string{}
	for _, m := range tr.Multipliers {
		got[m.Target] = m.Value
	}
	if got["core"] != "1.5" || got["tale"] != "2" || got["tale:prorab-1"] != "0.5" {
		t.Fatalf("multipliers: %v", got)
	}

	for name, target := range map[string]string{"unknown department": "nope", "unknown prorab": "tale:ghost", "empty": ""} {
		if err := s.e.SetTariff(s.ctx, admin, "multiplier", target, dec("2")); refusalCode(err) != codes.ValidationError {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := s.e.SetTariff(s.ctx, admin, "multiplier", "core", dec("0")); refusalCode(err) != codes.ValidationError {
		t.Errorf("zero multiplier: %v", err)
	}
}
