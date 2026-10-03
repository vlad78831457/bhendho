package contracts

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"offgrid/core/contracts"
)

func mustLoad(t *testing.T) *Validator {
	t.Helper()
	v, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return v
}

func readExample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := fs.ReadFile(contracts.FS, "manifests/examples/"+name)
	if err != nil {
		t.Fatalf("read example %s: %v", name, err)
	}
	return raw
}

// mutateBlank применяет правку к примеру бланка и возвращает JSON.
func mutateBlank(t *testing.T, edit func(b map[string]any)) []byte {
	t.Helper()
	var b map[string]any
	if err := json.Unmarshal(readExample(t, "taleweaver.generate_start.blank.json"), &b); err != nil {
		t.Fatal(err)
	}
	edit(b)
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func section(b map[string]any, key string) map[string]any { return b[key].(map[string]any) }

func TestExamplesAreValid(t *testing.T) {
	v := mustLoad(t)
	if err := v.ValidateManifestYAML(readExample(t, "taleweaver.module.yaml")); err != nil {
		t.Errorf("taleweaver manifest: %v", err)
	}
	if err := v.ValidateBlank(readExample(t, "taleweaver.generate_start.blank.json")); err != nil {
		t.Errorf("taleweaver blank: %v", err)
	}
}

func TestBlankRejections(t *testing.T) {
	v := mustLoad(t)
	cases := []struct {
		name string
		edit func(b map[string]any)
	}{
		{"verdict outside registry (ADR-48)", func(b map[string]any) {
			section(b, "report")["verdict"] = "EXECUTION_ERROR"
		}},
		{"fact status on task (ADR-48)", func(b map[string]any) {
			section(b, "system")["status"] = "published"
		}},
		{"task status on fact (ADR-48)", func(b map[string]any) {
			s := section(b, "system")
			s["kind"] = "fact"
			s["status"] = "pending"
		}},
		{"unknown system field", func(b map[string]any) {
			section(b, "system")["price"] = 1
		}},
		{"worker charge without lines (ADR-47)", func(b map[string]any) {
			section(b, "report")["worker_charge"] = map[string]any{"total_gc": 5}
		}},
		{"report without verdict", func(b map[string]any) {
			delete(section(b, "report"), "verdict")
		}},
		{"bad document_id format", func(b map[string]any) {
			section(b, "system")["document_id"] = "not-a-uuid"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.ValidateBlank(mutateBlank(t, tc.edit)); err == nil {
				t.Fatal("expected rejection, got nil")
			}
		})
	}
}

func TestBlankAcceptsEveryStatusOfItsKind(t *testing.T) {
	v := mustLoad(t)
	for kind, statuses := range map[string][]string{
		"task": {"draft", "template", "pending", "processing", "completed", "failed", "cancelled"},
		"fact": {"draft", "published", "archived", "deleted"},
	} {
		for _, st := range statuses {
			raw := mutateBlank(t, func(b map[string]any) {
				s := section(b, "system")
				s["kind"] = kind
				s["status"] = st
			})
			if err := v.ValidateBlank(raw); err != nil {
				t.Errorf("%s/%s: %v", kind, st, err)
			}
		}
	}
}

func TestManifestRejections(t *testing.T) {
	v := mustLoad(t)
	base := string(readExample(t, "taleweaver.module.yaml"))
	cases := []struct {
		name string
		from string
		to   string
	}{
		{"duplicate unit (ADR-47)", "name: tts_seconds", "name: images"},
		{"extra unit shadows base unit", "name: tts_seconds", "name: tokens"},
		{"in_process without entry_point", "mode: worker_service", "mode: in_process"},
		{"bad module_id", "module_id: taleweaver/story-engine", "module_id: Taleweaver Engine"},
		{"form_id without version", "form_id: taleweaver.character.v1", "form_id: taleweaver.character"},
		{"zero rate_to_base", "rate_to_base: 150", "rate_to_base: 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(base, tc.from) {
				t.Fatalf("example has no %q", tc.from)
			}
			raw := []byte(strings.Replace(base, tc.from, tc.to, 1))
			if err := v.ValidateManifestYAML(raw); err == nil {
				t.Fatal("expected rejection, got nil")
			}
		})
	}
}

func TestEvents(t *testing.T) {
	v := mustLoad(t)
	valid := `{"event_id":"2b0c7a8e-5d4f-4a3b-9c2d-1e0f9a8b7c6d","type":"task.progress","seq":42,` +
		`"occurred_at":"2026-09-28T10:01:00Z","payload":{"task_id":"t1","percent":30,"phase":"text_ready"}}`
	if err := v.ValidateEvent([]byte(valid)); err != nil {
		t.Errorf("valid progress: %v", err)
	}
	for name, raw := range map[string]string{
		"percent over 100": strings.Replace(valid, `"percent":30`, `"percent":130`, 1),
		"missing seq":      strings.Replace(valid, `"seq":42,`, ``, 1),
		"unknown type":     strings.Replace(valid, `task.progress`, `task.log`, 1),
		"status outside task dictionary": `{"event_id":"2b0c7a8e-5d4f-4a3b-9c2d-1e0f9a8b7c6d","type":"task.status","seq":1,` +
			`"occurred_at":"2026-09-28T10:01:00Z","payload":{"task_id":"t1","status":"published"}}`,
	} {
		if err := v.ValidateEvent([]byte(raw)); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}
