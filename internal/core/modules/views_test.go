package modules

import (
	"strings"
	"testing"
)

// ADR-72: ссылки разметки проверяются при загрузке паспорта.
func TestViewReferences(t *testing.T) {
	forms := []FormRef{{FormID: "x.note.v1", Kind: "fact"}, {FormID: "x.other.v1", Kind: "fact"}, {FormID: "x.run.v1", Kind: "task"}}
	card := View{ID: "note.card", Kind: "card", Form: "x.note.v1", Body: []map[string]any{{"title": "name"}}}
	ok := Manifest{FormsProvided: forms, Views: []View{
		card,
		{ID: "note.section", Kind: "section", Title: "Мои", Form: "x.note.v1", Card: "note.card"},
		{ID: "go", Kind: "menu", Title: "Запуск", Target: map[string]string{"form": "x.run.v1"}},
		{ID: "mine", Kind: "menu", Title: "Мои", Target: map[string]string{"section": "note.section"}},
	}}
	if err := checkViews(ok); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		views []View
		want  string
	}{
		"duplicate id":                 {[]View{card, card}, "duplicate"},
		"card on a task":               {[]View{{ID: "c", Kind: "card", Form: "x.run.v1"}}, "not a fact form"},
		"card on a stranger":           {[]View{{ID: "c", Kind: "card", Form: "y.note.v1"}}, "not a fact form"},
		"section card of another form": {[]View{card, {ID: "s", Kind: "section", Form: "x.other.v1", Card: "note.card"}}, "must be a card"},
		"continue to a fact": {[]View{{ID: "c", Kind: "card", Form: "x.note.v1",
			Body: []map[string]any{{"action": "continue", "form": "x.other.v1"}}}}, "continue form"},
		"menu to a fact":     {[]View{{ID: "m", Kind: "menu", Target: map[string]string{"form": "x.note.v1"}}}, "target form"},
		"menu to no section": {[]View{{ID: "m", Kind: "menu", Target: map[string]string{"section": "nope"}}}, "target section"},
	} {
		err := checkViews(Manifest{FormsProvided: forms, Views: c.views})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
