package modules

import (
	"testing"

	"offgrid/core/internal/core/contracts"
)

// Заготовка подразделения (ADR-71) всегда проходит проверку ядра: из неё начинают новые подразделения.
func TestDepartmentTemplateIsValid(t *testing.T) {
	v, err := contracts.Load()
	if err != nil {
		t.Fatal(err)
	}
	m, err := Load("../../../templates/department/module", v)
	if err != nil {
		t.Fatal(err)
	}
	if m.TargetService() != "example" || len(m.Forms) != 3 {
		t.Fatalf("template: %s, %d forms", m.TargetService(), len(m.Forms))
	}
	audience := map[string]string{}
	for _, f := range m.Forms {
		audience[f.Ref.FormID] = f.Ref.AudienceOrUser()
	}
	if audience["example.spend.v1"] != "adm" || audience["example.note.v1"] != "user" {
		t.Fatalf("audience: %v", audience)
	}
}
