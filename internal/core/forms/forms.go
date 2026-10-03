// Package forms — проверка values бланка по fields_rules шаблона (FR-FORM-9, ADR-35).
// Ядро проверяет только форму данных (типы, обязательность, границы), не смысл (ADR-49).
package forms

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Rule — правило поля (fieldRule из blank_envelope_schema.json).
type Rule struct {
	Type             string          `json:"type"`
	Required         bool            `json:"required,omitempty"`
	Options          []string        `json:"options,omitempty"`
	Min              *float64        `json:"min,omitempty"`
	Max              *float64        `json:"max,omitempty"`
	ExpectedFactKind string          `json:"expected_fact_kind,omitempty"`
	AllowedFileTypes []string        `json:"allowed_file_types,omitempty"`
	Items            map[string]Rule `json:"items,omitempty"`
}

// Rules — fields_rules формы.
type Rules map[string]Rule

// FieldError — замечание к полю (подбланк ошибок, ADR-41).
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Ref — ссылка из бланка на факт или файл; её проверяют реестры (ADR-15/16).
type Ref struct {
	Field        string
	ID           uuid.UUID
	Required     bool
	ExpectedKind string   // только для fact_ref
	AllowedTypes []string // только для file: допустимые MIME (пусто — любые)
}

// Refs — все ссылки бланка.
type Refs struct {
	Facts []Ref
	Files []Ref
}

// MaxGroupDepth — потолок вложенности типов-расширений (ADR-35: «числа в конфиге»).
const MaxGroupDepth = 5

// CheckRules проверяет сам шаблон при регистрации формы: известные типы,
// select с вариантами, списки только на верхнем уровне (ADR-35).
func CheckRules(rules Rules) error {
	return checkRules(rules, 0, "")
}

func checkRules(rules Rules, depth int, prefix string) error {
	if depth > MaxGroupDepth {
		return fmt.Errorf("%s: nesting deeper than %d", prefix, MaxGroupDepth)
	}
	for name, r := range rules {
		path := join(prefix, name)
		switch r.Type {
		case "string", "text", "int", "float", "boolean", "date", "file", "mix":
		case "fact_ref":
			if r.ExpectedFactKind == "" {
				return fmt.Errorf("%s: fact_ref requires expected_fact_kind", path)
			}
		case "select", "multiselect":
			if len(r.Options) == 0 {
				return fmt.Errorf("%s: %s requires options", path, r.Type)
			}
		case "group":
			if len(r.Items) == 0 {
				return fmt.Errorf("%s: group requires items", path)
			}
			if err := checkNoLists(r.Items, path); err != nil {
				return err
			}
			if err := checkRules(r.Items, depth+1, path); err != nil {
				return err
			}
		case "list":
			if depth > 0 {
				return fmt.Errorf("%s: lists are allowed only at the top level", path)
			}
			if len(r.Items) == 0 {
				return fmt.Errorf("%s: list requires items", path)
			}
			if err := checkNoLists(r.Items, path); err != nil {
				return err
			}
			if err := checkRules(r.Items, depth+1, path); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: unknown type %q", path, r.Type)
		}
	}
	return nil
}

func checkNoLists(items Rules, path string) error {
	for n, r := range items {
		if r.Type == "list" {
			return fmt.Errorf("%s.%s: nested lists are not allowed", path, n)
		}
	}
	return nil
}

// Validate проверяет values и собирает ссылки. values декодированы с UseNumber.
func Validate(rules Rules, values map[string]any) ([]FieldError, Refs) {
	v := &validator{}
	v.object(rules, values, "")
	sort.Slice(v.errs, func(i, j int) bool { return v.errs[i].Field < v.errs[j].Field })
	return v.errs, v.refs
}

type validator struct {
	errs []FieldError
	refs Refs
}

func (v *validator) fail(field, format string, args ...any) {
	v.errs = append(v.errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) object(rules Rules, values map[string]any, prefix string) {
	for name := range values {
		if _, ok := rules[name]; !ok {
			v.fail(join(prefix, name), "unknown field")
		}
	}
	for name, r := range rules {
		path := join(prefix, name)
		val, present := values[name]
		if !present || val == nil {
			if r.Required {
				v.fail(path, "required")
			}
			continue
		}
		v.value(r, val, path)
	}
}

func (v *validator) value(r Rule, val any, path string) {
	switch r.Type {
	case "string", "text":
		s, ok := val.(string)
		if !ok {
			v.fail(path, "must be a string")
			return
		}
		v.bounds(path, float64(utf8.RuneCountInString(s)), r, "length")
	case "int":
		n, ok := val.(json.Number)
		if !ok {
			v.fail(path, "must be an integer")
			return
		}
		i, err := n.Int64()
		if err != nil {
			v.fail(path, "must be an integer")
			return
		}
		v.bounds(path, float64(i), r, "value")
	case "float":
		n, ok := val.(json.Number)
		if !ok {
			v.fail(path, "must be a number")
			return
		}
		f, err := n.Float64()
		if err != nil {
			v.fail(path, "must be a number")
			return
		}
		v.bounds(path, f, r, "value")
	case "boolean":
		if _, ok := val.(bool); !ok {
			v.fail(path, "must be a boolean")
		}
	case "date":
		s, ok := val.(string)
		if !ok {
			v.fail(path, "must be a date YYYY-MM-DD")
			return
		}
		if _, err := time.Parse(time.DateOnly, s); err != nil {
			v.fail(path, "must be a date YYYY-MM-DD")
		}
	case "select":
		s, ok := val.(string)
		if !ok || !contains(r.Options, s) {
			v.fail(path, "must be one of the options")
		}
	case "multiselect":
		arr, ok := val.([]any)
		if !ok {
			v.fail(path, "must be a list of options")
			return
		}
		seen := map[string]bool{}
		for _, e := range arr {
			s, ok := e.(string)
			if !ok || !contains(r.Options, s) || seen[s] {
				v.fail(path, "must be a list of distinct options")
				return
			}
			seen[s] = true
		}
		v.bounds(path, float64(len(arr)), r, "count")
	case "fact_ref", "file":
		s, ok := val.(string)
		id, err := uuid.Parse(s)
		if !ok || err != nil {
			v.fail(path, "must be an id")
			return
		}
		ref := Ref{Field: path, ID: id, Required: r.Required, ExpectedKind: r.ExpectedFactKind, AllowedTypes: r.AllowedFileTypes}
		if r.Type == "fact_ref" {
			v.refs.Facts = append(v.refs.Facts, ref)
		} else {
			v.refs.Files = append(v.refs.Files, ref)
		}
	case "group":
		obj, ok := val.(map[string]any)
		if !ok {
			v.fail(path, "must be an object")
			return
		}
		v.object(r.Items, obj, path)
	case "list":
		arr, ok := val.([]any)
		if !ok {
			v.fail(path, "must be a list")
			return
		}
		v.bounds(path, float64(len(arr)), r, "count")
		for i, e := range arr {
			obj, ok := e.(map[string]any)
			ep := fmt.Sprintf("%s[%d]", path, i)
			if !ok {
				v.fail(ep, "must be an object")
				continue
			}
			v.object(r.Items, obj, ep)
		}
	case "mix":
		// ADR-35: микс уходит как написан — не проверяется и не материализуется.
	default:
		v.fail(path, "unknown field type %q", r.Type)
	}
}

func (v *validator) bounds(path string, x float64, r Rule, what string) {
	if r.Min != nil && x < *r.Min {
		v.fail(path, "%s must be at least %v", what, *r.Min)
	}
	if r.Max != nil && x > *r.Max {
		v.fail(path, "%s must be at most %v", what, *r.Max)
	}
}

func contains(opts []string, s string) bool {
	for _, o := range opts {
		if o == s {
			return true
		}
	}
	return false
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}
