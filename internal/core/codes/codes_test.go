package codes

import (
	"encoding/json"
	"io/fs"
	"sort"
	"testing"

	"offgrid/core/contracts"
)

// Реестр в коде и enum вердикта в схеме конверта не должны разъехаться (ADR-48).
func TestRegistryMatchesEnvelopeSchema(t *testing.T) {
	raw, err := fs.ReadFile(contracts.FS, "manifests/blank_envelope_schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs struct {
			Verdict struct {
				Enum []string `json:"enum"`
			} `json:"verdict"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	want := schema.Defs.Verdict.Enum
	var got []string
	for _, c := range All() {
		got = append(got, string(c))
	}
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("schema %v != registry %v", want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("schema %v != registry %v", want, got)
		}
	}
}

func TestEveryCodeHasFullEntry(t *testing.T) {
	for _, c := range All() {
		e, _ := Lookup(c)
		if e.Stage == "" || e.Healer == "" || e.Class == "" || e.Message == "" {
			t.Errorf("%s: incomplete registry entry %+v", c, e)
		}
	}
}

func TestRetryable(t *testing.T) {
	for c, want := range map[Code]bool{
		WorkerError: true, LimitExhausted: true, ReportInvalid: true,
		FactMissing: false, FileMissing: false, WorkerTimeout: false, DataRejected: false, Code("NOPE"): false,
	} {
		if got := Retryable(c); got != want {
			t.Errorf("Retryable(%s) = %v, want %v", c, got, want)
		}
	}
}
