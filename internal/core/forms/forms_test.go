package forms

import (
	"encoding/json"
	"strings"
	"testing"
)

func f(x float64) *float64 { return &x }

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// «Протокол осмотра»: список замечаний из расширения (ADR-35).
var inspection = Rules{
	"title":   {Type: "string", Required: true, Min: f(1), Max: f(20)},
	"visits":  {Type: "int", Min: f(0), Max: f(10)},
	"score":   {Type: "float"},
	"urgent":  {Type: "boolean"},
	"on":      {Type: "date"},
	"level":   {Type: "select", Options: []string{"low", "high"}},
	"tags":    {Type: "multiselect", Options: []string{"a", "b"}},
	"hero":    {Type: "fact_ref", Required: true, ExpectedFactKind: "tale.character.v1"},
	"photo":   {Type: "file"},
	"address": {Type: "group", Items: Rules{"city": {Type: "string", Required: true}}},
	"notes": {Type: "list", Min: f(1), Max: f(2), Items: Rules{
		"text": {Type: "text", Required: true},
		"pic":  {Type: "file", Required: true},
	}},
	"extra": {Type: "mix"},
}

const valid = `{
	"title": "Осмотр", "visits": 3, "score": 4.5, "urgent": true, "on": "2026-09-28",
	"level": "high", "tags": ["a", "b"],
	"hero": "0b6c1f4e-8f0e-4a55-b2a7-3e9d1c7f2a01",
	"photo": "1b6c1f4e-8f0e-4a55-b2a7-3e9d1c7f2a02",
	"address": {"city": "Сочи"},
	"notes": [{"text": "трещина", "pic": "2b6c1f4e-8f0e-4a55-b2a7-3e9d1c7f2a03"}],
	"extra": {"anything": [1, "two"]}
}`

func TestValidateValid(t *testing.T) {
	errs, refs := Validate(inspection, decode(t, valid))
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(refs.Facts) != 1 || refs.Facts[0].ExpectedKind != "tale.character.v1" || !refs.Facts[0].Required {
		t.Fatalf("fact refs: %+v", refs.Facts)
	}
	if len(refs.Files) != 2 {
		t.Fatalf("file refs: %+v", refs.Files)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]struct {
		edit  func(m map[string]any)
		field string
	}{
		"missing required":    {func(m map[string]any) { delete(m, "hero") }, "hero"},
		"null required":       {func(m map[string]any) { m["title"] = nil }, "title"},
		"string too long":     {func(m map[string]any) { m["title"] = strings.Repeat("я", 21) }, "title"},
		"int not integer":     {func(m map[string]any) { m["visits"] = json.Number("1.5") }, "visits"},
		"int above max":       {func(m map[string]any) { m["visits"] = json.Number("11") }, "visits"},
		"float not number":    {func(m map[string]any) { m["score"] = "4.5" }, "score"},
		"bad date":            {func(m map[string]any) { m["on"] = "28.09.2026" }, "on"},
		"select outside":      {func(m map[string]any) { m["level"] = "mid" }, "level"},
		"multiselect dup":     {func(m map[string]any) { m["tags"] = []any{"a", "a"} }, "tags"},
		"fact_ref not uuid":   {func(m map[string]any) { m["hero"] = "Р-014" }, "hero"},
		"unknown field":       {func(m map[string]any) { m["price"] = json.Number("1") }, "price"},
		"group missing inner": {func(m map[string]any) { m["address"] = map[string]any{} }, "address.city"},
		"list too short":      {func(m map[string]any) { m["notes"] = []any{} }, "notes"},
		"list element field":  {func(m map[string]any) { m["notes"] = []any{map[string]any{"text": "x"}} }, "notes[0].pic"},
		"boolean as string":   {func(m map[string]any) { m["urgent"] = "yes" }, "urgent"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := decode(t, valid)
			tc.edit(m)
			errs, _ := Validate(inspection, m)
			for _, e := range errs {
				if e.Field == tc.field {
					return
				}
			}
			t.Fatalf("want error on %q, got %v", tc.field, errs)
		})
	}
}

func TestMixIsNotValidated(t *testing.T) {
	m := decode(t, valid)
	m["extra"] = []any{json.Number("1"), map[string]any{"x": nil}}
	if errs, _ := Validate(inspection, m); len(errs) != 0 {
		t.Fatalf("mix must pass as written: %v", errs)
	}
}

func TestCheckRules(t *testing.T) {
	if err := CheckRules(inspection); err != nil {
		t.Fatalf("valid rules: %v", err)
	}
	bad := map[string]Rules{
		"unknown type":      {"x": {Type: "money"}},
		"select no options": {"x": {Type: "select"}},
		"fact_ref no kind":  {"x": {Type: "fact_ref"}},
		"list in group":     {"g": {Type: "group", Items: Rules{"l": {Type: "list", Items: Rules{"a": {Type: "text"}}}}}},
		"list in list":      {"l": {Type: "list", Items: Rules{"l2": {Type: "list", Items: Rules{"a": {Type: "text"}}}}}},
		"empty group":       {"g": {Type: "group"}},
	}
	for name, r := range bad {
		if err := CheckRules(r); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestRewriteReplacesRefsWithoutMutation(t *testing.T) {
	m := decode(t, valid)
	calls := map[string]int{}
	out, err := Rewrite(inspection, m, func(typ string, ref Ref) (any, error) {
		calls[typ]++
		return map[string]any{"resolved": ref.ID.String(), "field": ref.Field}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls["fact_ref"] != 1 || calls["file"] != 2 {
		t.Fatalf("calls = %v", calls)
	}
	if _, ok := m["hero"].(string); !ok {
		t.Fatal("input values were mutated")
	}
	hero := out["hero"].(map[string]any)
	if hero["field"] != "hero" {
		t.Fatalf("hero = %v", hero)
	}
	pic := out["notes"].([]any)[0].(map[string]any)["pic"].(map[string]any)
	if pic["field"] != "notes[0].pic" {
		t.Fatalf("pic = %v", pic)
	}
	if out["extra"].(map[string]any)["anything"] == nil {
		t.Fatal("mix must be copied as written")
	}
}
