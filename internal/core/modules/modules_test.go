package modules

import "testing"

func TestResultSpecChecks(t *testing.T) {
	body := []byte(`{"title": "T", "fields_rules": {
		"story": {"type": "fact_ref", "expected_fact_kind": "s.state.v1"},
		"note":  {"type": "string"}}}`)
	cases := map[string]struct {
		ref FormRef
		ok  bool
	}{
		"new fact":            {FormRef{FormID: "s.a.v1", Kind: "task", Result: &ResultSpec{FactKind: "s.state.v1"}}, true},
		"versions fact_ref":   {FormRef{FormID: "s.a.v1", Kind: "task", Result: &ResultSpec{FactKind: "s.state.v1", VersionsField: "story"}}, true},
		"versions non-ref":    {FormRef{FormID: "s.a.v1", Kind: "task", Result: &ResultSpec{FactKind: "s.state.v1", VersionsField: "note"}}, false},
		"versions other kind": {FormRef{FormID: "s.a.v1", Kind: "task", Result: &ResultSpec{FactKind: "s.other.v1", VersionsField: "story"}}, false},
		"versions missing":    {FormRef{FormID: "s.a.v1", Kind: "task", Result: &ResultSpec{FactKind: "s.state.v1", VersionsField: "nope"}}, false},
		"result on fact form": {FormRef{FormID: "s.a.v1", Kind: "fact", Result: &ResultSpec{FactKind: "s.state.v1"}}, false},
	}
	for name, tc := range cases {
		if _, err := ParseForm(tc.ref, body); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, tc.ok)
		}
	}
}
