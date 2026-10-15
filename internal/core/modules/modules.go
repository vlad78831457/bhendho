// Package modules загружает паспорта модулей (module.yaml) и их формы с диска.
// Регистрацию в БД делает engine.RegisterModule (ADR-34: формы даёт сторона исполнения).
package modules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/shopspring/decimal"
	"gopkg.in/yaml.v3"

	"offgrid/core/internal/core/contracts"
	"offgrid/core/internal/core/forms"
)

// Manifest — поля паспорта, нужные ядру.
type Manifest struct {
	ModuleID string `yaml:"module_id"`
	Name     string `yaml:"name"`
	Version  string `yaml:"version"`
	Mode     string `yaml:"mode"`
	Worker   *struct {
		Runtime       string `yaml:"runtime"`
		Image         string `yaml:"image"`
		TargetService string `yaml:"target_service"`
	} `yaml:"worker"`
	FormsProvided []FormRef   `yaml:"forms_provided"`
	Accounting    *Accounting `yaml:"worker_accounting"`
	Views         []View      `yaml:"views"` // ADR-72
}

// FormRef — ссылка паспорта на файл формы.
type FormRef struct {
	FormID     string          `yaml:"form_id"`
	Kind       string          `yaml:"kind"`
	Title      string          `yaml:"title"`
	SchemaFile string          `yaml:"schema_file"`
	BasePrice  decimal.Decimal `yaml:"base_price_gc"`
	Result     *ResultSpec     `yaml:"result"`
	Audience   string          `yaml:"audience"`   // ADR-65: пусто — user
	UserMarks  []string        `yaml:"user_marks"` // ADR-68: пометки пользователя в снимок задачи
}

// ResultSpec — факт-результат задачи (ADR-51).
type ResultSpec struct {
	FactKind      string `yaml:"fact_kind" json:"fact_kind"`
	VersionsField string `yaml:"versions_field" json:"versions_field,omitempty"`
	Publish       string `yaml:"publish" json:"publish,omitempty"`
}

// AutoPublish — версия результата сразу опубликована.
func (r *ResultSpec) AutoPublish() bool { return r != nil && r.Publish == "auto" }

// Accounting — единицы учёта воркера (ADR-47).
type Accounting struct {
	BaseUnit struct {
		Name   string          `yaml:"name" json:"name"`
		RateGC decimal.Decimal `yaml:"rate_gc" json:"rate_gc"`
	} `yaml:"base_unit" json:"base_unit"`
	ExtraUnits []struct {
		Name       string          `yaml:"name" json:"name"`
		RateToBase decimal.Decimal `yaml:"rate_to_base" json:"rate_to_base"`
	} `yaml:"extra_units" json:"extra_units,omitempty"`
}

// Form — загруженная форма: meta_ui целиком и разобранные правила полей.
type Form struct {
	Ref    FormRef
	MetaUI map[string]any
	Rules  forms.Rules
}

// Module — паспорт и формы модуля.
type Module struct {
	Dir      string
	Manifest Manifest
	Forms    []Form
}

// TargetService — адресат очереди модуля.
func (m Module) TargetService() string {
	if m.Manifest.Worker == nil {
		return ""
	}
	return m.Manifest.Worker.TargetService
}

// LoadDir читает все <dir>/*/module.yaml.
func LoadDir(dir string, v *contracts.Validator) ([]Module, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*", "module.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make([]Module, 0, len(paths))
	for _, p := range paths {
		m, err := Load(filepath.Dir(p), v)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// Load читает один модуль с диска: проверяет паспорт по схеме и каждую форму по правилам ADR-35.
func Load(dir string, v *contracts.Validator) (Module, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "module.yaml"))
	if err != nil {
		return Module{}, err
	}
	return parse(dir, raw, func(path string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, filepath.Clean(path))) }, v)
}

// Parse — модуль, присланный подразделением при самостоятельной регистрации (ADR-70): паспорт и файлы форм
// по путям из schema_file.
func Parse(label string, manifest []byte, files map[string][]byte, v *contracts.Validator) (Module, error) {
	return parse(label, manifest, func(path string) ([]byte, error) {
		if body, ok := files[path]; ok {
			return body, nil
		}
		return nil, fmt.Errorf("file %q is not sent", path)
	}, v)
}

func parse(dir string, raw []byte, read func(path string) ([]byte, error), v *contracts.Validator) (Module, error) {
	if err := v.ValidateManifestYAML(raw); err != nil {
		return Module{}, fmt.Errorf("%s: %w", dir, err)
	}
	var man Manifest
	if err := yaml.Unmarshal(raw, &man); err != nil {
		return Module{}, fmt.Errorf("%s: %w", dir, err)
	}
	if err := checkViews(man); err != nil {
		return Module{}, fmt.Errorf("%s: %w", dir, err)
	}
	mod := Module{Dir: dir, Manifest: man}
	for _, ref := range man.FormsProvided {
		body, err := read(ref.SchemaFile)
		if err != nil {
			return Module{}, fmt.Errorf("%s: form %s: %w", dir, ref.FormID, err)
		}
		f, err := ParseForm(ref, body)
		if err != nil {
			return Module{}, fmt.Errorf("%s: %w", dir, err)
		}
		mod.Forms = append(mod.Forms, f)
	}
	return mod, nil
}

// ParseForm разбирает файл формы: {title, category, description, fields_rules, …} = meta_ui.
func ParseForm(ref FormRef, body []byte) (Form, error) {
	var meta map[string]any
	if err := json.Unmarshal(body, &meta); err != nil {
		return Form{}, fmt.Errorf("form %s: %w", ref.FormID, err)
	}
	if _, ok := meta["title"]; !ok {
		meta["title"] = ref.Title
	}
	rawRules, err := json.Marshal(meta["fields_rules"])
	if err != nil {
		return Form{}, err
	}
	var rules forms.Rules
	if err := json.Unmarshal(rawRules, &rules); err != nil || rules == nil {
		return Form{}, fmt.Errorf("form %s: fields_rules must be an object", ref.FormID)
	}
	if err := forms.CheckRules(rules); err != nil {
		return Form{}, fmt.Errorf("form %s: %w", ref.FormID, err)
	}
	if err := checkResult(ref, rules); err != nil {
		return Form{}, err
	}
	return Form{Ref: ref, MetaUI: meta, Rules: rules}, nil
}

// checkResult: результат объявляют только формы задач; versions_field — fact_ref
// верхнего уровня на факт того же kind, что и результат (ADR-51).
func checkResult(ref FormRef, rules forms.Rules) error {
	r := ref.Result
	if r == nil {
		return nil
	}
	if ref.Kind != "task" {
		return fmt.Errorf("form %s: result is allowed only for task forms", ref.FormID)
	}
	if r.VersionsField == "" {
		return nil
	}
	f, ok := rules[r.VersionsField]
	if !ok || f.Type != "fact_ref" || f.ExpectedFactKind != r.FactKind {
		return fmt.Errorf("form %s: versions_field %q must be a fact_ref to %s", ref.FormID, r.VersionsField, r.FactKind)
	}
	return nil
}

// AudienceOrUser — «кому» факт формы (ADR-65); не указано — пользователю.
func (r FormRef) AudienceOrUser() string {
	if r.Audience == "" {
		return "user"
	}
	return r.Audience
}

// View — разметка общей территории фронта (ADR-72): card, section или menu. Ядро проверяет форму и ссылки,
// смысла полей не знает; рисует оболочка.
type View struct {
	ID     string            `yaml:"id" json:"id"`
	Kind   string            `yaml:"kind" json:"kind"`
	Form   string            `yaml:"form,omitempty" json:"form,omitempty"`
	Title  string            `yaml:"title,omitempty" json:"title,omitempty"`
	Body   []map[string]any  `yaml:"body,omitempty" json:"body,omitempty"`
	Card   string            `yaml:"card,omitempty" json:"card,omitempty"`
	Order  string            `yaml:"order,omitempty" json:"order,omitempty"`
	Icon   string            `yaml:"icon,omitempty" json:"icon,omitempty"`
	Target map[string]string `yaml:"target,omitempty" json:"target,omitempty"`
}

// checkViews — ссылки разметки: id уникальны; card и section — на факт-форму этого паспорта; section — на card той же
// формы; continue и menu.target.form — на задачу этого паспорта; menu.target.section — на раздел.
func checkViews(m Manifest) error {
	kinds := map[string]string{}
	for _, f := range m.FormsProvided {
		kinds[f.FormID] = f.Kind
	}
	views := map[string]View{}
	for _, v := range m.Views {
		if _, dup := views[v.ID]; dup {
			return fmt.Errorf("view %q: duplicate id", v.ID)
		}
		views[v.ID] = v
	}
	for _, v := range m.Views {
		if (v.Kind == "card" || v.Kind == "section") && kinds[v.Form] != "fact" {
			return fmt.Errorf("view %q: form %q is not a fact form of this passport", v.ID, v.Form)
		}
		for _, el := range v.Body {
			if el["action"] == "continue" && kinds[fmt.Sprint(el["form"])] != "task" {
				return fmt.Errorf("view %q: continue form %v is not a task form of this passport", v.ID, el["form"])
			}
		}
		switch v.Kind {
		case "section":
			c, ok := views[v.Card]
			if !ok || c.Kind != "card" || c.Form != v.Form {
				return fmt.Errorf("view %q: card %q must be a card of form %q", v.ID, v.Card, v.Form)
			}
		case "menu":
			if f, ok := v.Target["form"]; ok && kinds[f] != "task" {
				return fmt.Errorf("view %q: target form %q is not a task form of this passport", v.ID, f)
			}
			if s, ok := v.Target["section"]; ok && views[s].Kind != "section" {
				return fmt.Errorf("view %q: target section %q is not a section", v.ID, s)
			}
		}
	}
	return nil
}
