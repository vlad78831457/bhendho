package forms

import (
	"fmt"

	"github.com/google/uuid"
)

// Resolver подставляет содержимое вместо ссылки: typ — "fact_ref" или "file".
type Resolver func(typ string, ref Ref) (any, error)

// Rewrite возвращает новую копию values, где ссылки fact_ref/file заменены результатом
// resolve; остальное копируется как написано, «микс» — без изменений (ADR-35).
// Исходные values не мутируются (ADR-10). Ожидает values, уже прошедшие Validate.
func Rewrite(rules Rules, values map[string]any, resolve Resolver) (map[string]any, error) {
	return rewriteObject(rules, values, "", resolve)
}

func rewriteObject(rules Rules, values map[string]any, prefix string, resolve Resolver) (map[string]any, error) {
	out := make(map[string]any, len(values))
	for name, val := range values {
		r, ok := rules[name]
		if !ok || val == nil {
			out[name] = val
			continue
		}
		nv, err := rewriteValue(r, val, join(prefix, name), resolve)
		if err != nil {
			return nil, err
		}
		out[name] = nv
	}
	return out, nil
}

func rewriteValue(r Rule, val any, path string, resolve Resolver) (any, error) {
	switch r.Type {
	case "fact_ref", "file":
		s, _ := val.(string)
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("%s: not an id", path)
		}
		return resolve(r.Type, Ref{Field: path, ID: id, Required: r.Required, ExpectedKind: r.ExpectedFactKind})
	case "group":
		obj, ok := val.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: not an object", path)
		}
		return rewriteObject(r.Items, obj, path, resolve)
	case "list":
		arr, ok := val.([]any)
		if !ok {
			return nil, fmt.Errorf("%s: not a list", path)
		}
		out := make([]any, len(arr))
		for i, e := range arr {
			obj, ok := e.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s[%d]: not an object", path, i)
			}
			nv, err := rewriteObject(r.Items, obj, fmt.Sprintf("%s[%d]", path, i), resolve)
			if err != nil {
				return nil, err
			}
			out[i] = nv
		}
		return out, nil
	case "multiselect":
		arr, _ := val.([]any)
		return append([]any(nil), arr...), nil
	default:
		return val, nil
	}
}
