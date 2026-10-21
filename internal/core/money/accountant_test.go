package money

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// Курсы из примера Taleweaver: tokens — основная, images/tts_seconds — производные.
func taleweaver() *Rates {
	return &Rates{
		BaseUnit: "tokens",
		RateGC:   d("0.001"),
		ToBase:   map[string]decimal.Decimal{"images": d("20000"), "tts_seconds": d("150")},
	}
}

func TestSettleMultiUnit(t *testing.T) {
	bill, err := Settle([]Line{
		{Unit: "tokens", Units: d("12400")},
		{Unit: "images", Units: d("4")},
		{Unit: "tts_seconds", Units: d("96")},
	}, taleweaver(), d("150"), d("1"))
	if err != nil {
		t.Fatal(err)
	}
	// 12400 + 4×20000 + 96×150 = 106800 base → 106.8 GC
	if !bill.BaseUnits.Equal(d("106800")) || !bill.Total.Equal(d("106.8")) {
		t.Fatalf("bill = %+v", bill)
	}
	if !bill.Charged.Equal(d("106.8")) || !bill.Released.Equal(d("43.2")) {
		t.Fatalf("charged/released = %s/%s", bill.Charged, bill.Released)
	}
}

func TestSettleCappedByReserve(t *testing.T) {
	bill, err := Settle([]Line{{Unit: "images", Units: d("100")}}, taleweaver(), d("150"), d("1"))
	if err != nil {
		t.Fatal(err)
	}
	if !bill.Total.Equal(d("2000")) || !bill.Charged.Equal(d("150")) || !bill.Released.IsZero() || !bill.Over.IsZero() {
		t.Fatalf("bill = %+v", bill)
	}
}

// ADR-63: до полутора резервов; сверх резерва — Over, резерв не возвращается.
func TestSettleOverdraftFactor(t *testing.T) {
	// 180 GC при резерве 150 и множителе 1.5: списано всё, 30 — сверх резерва.
	bill, err := Settle([]Line{{Unit: "images", Units: d("9")}}, taleweaver(), d("150"), d("1.5"))
	if err != nil {
		t.Fatal(err)
	}
	if !bill.Charged.Equal(d("180")) || !bill.Over.Equal(d("30")) || !bill.Released.IsZero() {
		t.Fatalf("within factor: %+v", bill)
	}
	// 2000 GC — упирается в крышку 225.
	bill, err = Settle([]Line{{Unit: "images", Units: d("100")}}, taleweaver(), d("150"), d("1.5"))
	if err != nil {
		t.Fatal(err)
	}
	if !bill.Charged.Equal(d("225")) || !bill.Over.Equal(d("75")) || !bill.Released.IsZero() {
		t.Fatalf("capped: %+v", bill)
	}
	// Меньше резерва — как раньше: остаток возвращается, сверх резерва ничего.
	bill, err = Settle([]Line{{Unit: "tokens", Units: d("100000")}}, taleweaver(), d("150"), d("1.5"))
	if err != nil {
		t.Fatal(err)
	}
	if !bill.Charged.Equal(d("100")) || !bill.Released.Equal(d("50")) || !bill.Over.IsZero() {
		t.Fatalf("under reserve: %+v", bill)
	}
}

func TestSettleFixedPrice(t *testing.T) {
	bill, err := Settle(nil, nil, d("25"), d("1.5"))
	if err != nil || !bill.Charged.Equal(d("25")) || !bill.Released.IsZero() {
		t.Fatalf("bill = %+v err = %v", bill, err)
	}
}

func TestSettleRejects(t *testing.T) {
	if _, err := Settle([]Line{{Unit: "gpu_seconds", Units: d("1")}}, taleweaver(), d("10"), d("1")); !errors.Is(err, ErrUnknownUnit) {
		t.Errorf("unknown unit: err = %v", err)
	}
	if _, err := Settle([]Line{{Unit: "tokens", Units: d("-1")}}, taleweaver(), d("10"), d("1")); err == nil {
		t.Error("negative units must be rejected")
	}
	if _, err := Settle(nil, taleweaver(), d("10"), d("1")); err == nil {
		t.Error("metered service without charge must be rejected")
	}
}

func TestSettleRoundsToMoneyScale(t *testing.T) {
	bill, err := Settle([]Line{{Unit: "tokens", Units: d("1")}}, &Rates{BaseUnit: "tokens", RateGC: d("0.00001")}, d("1"), d("1"))
	if err != nil {
		t.Fatal(err)
	}
	if !bill.Total.Equal(d("0")) || !bill.Released.Equal(d("1")) {
		t.Fatalf("bill = %+v", bill)
	}
}

// ADR-78: коэффициенты ядра × подразделения × прораба умножают факт; единицы воркера — как есть.
func TestSettleMultipliers(t *testing.T) {
	lines := []Line{{Unit: "tokens", Units: d("10000")}} // 10 GC без коэффициентов
	r := taleweaver()
	if r.K().String() != "1" {
		t.Fatalf("default k = %s", r.K())
	}
	m, pm := d("1.4"), d("0.5")
	r.Multiplier, r.ProrabMultiplier = &m, &pm
	bill, err := Settle(lines, r, d("150"), d("1"))
	if err != nil {
		t.Fatal(err)
	}
	if !bill.BaseUnits.Equal(d("10000")) || !bill.Total.Equal(d("7")) || !bill.Charged.Equal(d("7")) {
		t.Fatalf("bill = %+v", bill)
	}
	// Крышка по-прежнему в долях резерва.
	big := d("3")
	r.ProrabMultiplier = &big
	bill, _ = Settle([]Line{{Unit: "tokens", Units: d("100000")}}, r, d("150"), d("1.5"))
	if !bill.Charged.Equal(d("225")) || !bill.Over.Equal(d("75")) {
		t.Fatalf("capped bill = %+v", bill)
	}
}
