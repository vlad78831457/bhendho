// Package money — системный бухгалтер (ADR-29, ADR-47): переводит счёт воркера
// из его единиц в основную единицу и в валюту списания, с крышкой в долях резерва (ADR-63).
package money

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// Scale — точность денег в БД: NUMERIC(15,4).
const Scale = 4

// Rates — курсы единиц воркера, зафиксированные в задаче при создании.
type Rates struct {
	BaseUnit string                     `json:"base_unit"`
	RateGC   decimal.Decimal            `json:"rate_gc"`
	ToBase   map[string]decimal.Decimal `json:"to_base,omitempty"`
	// Overdraft — множитель крышки, действовавший при создании задачи (ADR-69); пусто — из настроек.
	Overdraft *decimal.Decimal `json:"overdraft_factor,omitempty"`
	// Коэффициенты цены (ADR-78): Multiplier — ядра × подразделения, при создании задачи (резерв тоже × него);
	// ProrabMultiplier — прораба, когда он берёт задачу. Пусто — 1. Воркер отчитывается как есть.
	Multiplier       *decimal.Decimal `json:"multiplier,omitempty"`
	Prorab           string           `json:"prorab,omitempty"`
	ProrabMultiplier *decimal.Decimal `json:"prorab_multiplier,omitempty"`
}

// Line — строка счёта воркера.
type Line struct {
	Unit  string          `json:"unit"`
	Units decimal.Decimal `json:"units"`
}

// Bill — результат сверки.
type Bill struct {
	BaseUnits decimal.Decimal // Σ units × rate_to_base
	Total     decimal.Decimal // base_units × rate_gc (факт)
	Charged   decimal.Decimal // min(факт, резерв × множитель)
	Released  decimal.Decimal // резерв − списано, если списано меньше резерва
	Over      decimal.Decimal // списано сверх резерва (ADR-63)
}

// ErrUnknownUnit — единица не объявлена в манифесте → REPORT_INVALID.
var ErrUnknownUnit = errors.New("unknown worker unit")

// Settle считает счёт: Σ единиц × курс × коэффициенты (ADR-78). rates == nil — фиксированная цена: факт = резерв (ADR-29 п.7).
// factor — крышка списания в долях резерва (ADR-63): 1 — не больше резерва, 1.5 — до полутора резервов.
func Settle(lines []Line, rates *Rates, reserve, factor decimal.Decimal) (Bill, error) {
	if rates == nil {
		return Bill{Total: reserve, Charged: reserve, Released: decimal.Zero}, nil
	}
	if len(lines) == 0 {
		return Bill{}, errors.New("worker charge is required for metered service")
	}
	base := decimal.Zero
	for _, l := range lines {
		if l.Units.IsNegative() {
			return Bill{}, fmt.Errorf("negative units for %q", l.Unit)
		}
		k, err := rates.toBase(l.Unit)
		if err != nil {
			return Bill{}, err
		}
		base = base.Add(l.Units.Mul(k))
	}
	total := base.Mul(rates.RateGC).Mul(rates.K()).Round(Scale)
	charged := decimal.Min(total, reserve.Mul(factor).Round(Scale))
	return Bill{
		BaseUnits: base,
		Total:     total,
		Charged:   charged,
		Released:  decimal.Max(reserve.Sub(charged), decimal.Zero),
		Over:      decimal.Max(charged.Sub(reserve), decimal.Zero),
	}, nil
}

func (r *Rates) toBase(unit string) (decimal.Decimal, error) {
	if unit == r.BaseUnit {
		return decimal.NewFromInt(1), nil
	}
	if k, ok := r.ToBase[unit]; ok {
		return k, nil
	}
	return decimal.Zero, fmt.Errorf("%w: %q", ErrUnknownUnit, unit)
}

// K — итоговый коэффициент цены: ядра × подразделения × прораба (ADR-78); не задан — 1.
func (r *Rates) K() decimal.Decimal {
	k := decimal.NewFromInt(1)
	if r.Multiplier != nil {
		k = k.Mul(*r.Multiplier)
	}
	if r.ProrabMultiplier != nil {
		k = k.Mul(*r.ProrabMultiplier)
	}
	return k
}
