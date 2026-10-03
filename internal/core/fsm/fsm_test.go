package fsm

import (
	"testing"

	"offgrid/core/internal/core/codes"
)

// Каждая клетка матрицы проверена явно (критерий приёмки 5.9).
func TestMatrixEveryCell(t *testing.T) {
	type cell struct {
		s Status
		c Command
		v codes.Code
	}
	allowed := map[cell]bool{
		{Pending, Cancel, ""}:                 true,
		{Failed, Retry, codes.WorkerError}:    true,
		{Failed, Retry, codes.LimitExhausted}: true,
		{Failed, Retry, codes.ReportInvalid}:  true,
	}
	verdicts := append([]codes.Code{""}, codes.All()...)
	for _, s := range Statuses {
		for _, c := range Commands {
			for _, v := range verdicts {
				want := allowed[cell{s, c, v}]
				if s == Pending && c == Cancel {
					want = true // cancel из очереди не зависит от вердикта
				}
				if got := Allowed(s, c, v); got != want {
					t.Errorf("Allowed(%s, %s, %q) = %v, want %v", s, c, v, got, want)
				}
			}
		}
	}
}

func TestUnknownCommandRefused(t *testing.T) {
	if Known("delete") || Allowed(Pending, "delete", "") {
		t.Fatal("unknown command must be refused")
	}
}

func TestEnvelope(t *testing.T) {
	e := Envelope(Pending, "")
	if e["cancel"]["status"] != "todo" || e["retry"]["status"] != "disabled" {
		t.Fatalf("pending envelope: %v", e)
	}
	e = Envelope(Failed, codes.FactMissing)
	if e["retry"]["status"] != "disabled" {
		t.Fatalf("failed by data must not offer retry: %v", e)
	}
}

func TestTerminal(t *testing.T) {
	for _, s := range Statuses {
		want := s == Completed || s == Failed || s == Cancelled
		if Terminal(s) != want {
			t.Errorf("Terminal(%s) = %v", s, !want)
		}
	}
}
