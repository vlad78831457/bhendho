package httpapi

// Договор API людей (ADR-80, contracts/interfaces/people.openapi.yaml): каждый ответ /api/v1 в e2e-тестах сверяется
// с договором — адрес и код ответа описаны, тело JSON подходит под схему. Переименовали поле, убрали обязательное,
// добавили адрес без договора — тест ядра красный, а не экран у человека. PEOPLE_RECORD=<файл> — записать ответы
// (черновик договора при большой правке); значения в записи — только тестовые.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const peopleContractPath = "../../../contracts/interfaces/people.openapi.yaml"

type peopleContract struct {
	doc      map[string]any
	compiler *jsonschema.Compiler
	mu       sync.Mutex
	schemas  map[string]*jsonschema.Schema
}

var (
	contractOnce sync.Once
	contractVal  *peopleContract
	contractErr  error
)

func loadPeopleContract() (*peopleContract, error) {
	contractOnce.Do(func() {
		raw, err := os.ReadFile(peopleContractPath)
		if err != nil {
			contractErr = err
			return
		}
		var doc map[string]any
		if err = yaml.Unmarshal(raw, &doc); err != nil {
			contractErr = err
			return
		}
		js, err := json.Marshal(doc)
		if err != nil {
			contractErr = err
			return
		}
		parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(js))
		if err != nil {
			contractErr = err
			return
		}
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		if err = c.AddResource("people.json", parsed); err != nil {
			contractErr = err
			return
		}
		contractVal = &peopleContract{doc: doc, compiler: c, schemas: map[string]*jsonschema.Schema{}}
	})
	return contractVal, contractErr
}

// pointer — JSON Pointer до части договора: «/» и «~» в именах экранируются.
func pointer(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString("/")
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(p))
	}
	return b.String()
}

func (c *peopleContract) operation(method, path string) map[string]any {
	paths, _ := c.doc["paths"].(map[string]any)
	item, _ := paths[path].(map[string]any)
	op, _ := item[strings.ToLower(method)].(map[string]any)
	return op
}

// check — подходит ли ответ под договор; пусто — подходит.
func (c *peopleContract) check(method, path string, status int, contentType string, body []byte) string {
	op := c.operation(method, path)
	if op == nil {
		return "адреса нет в договоре"
	}
	responses, _ := op["responses"].(map[string]any)
	code := strconv.Itoa(status)
	resp, ok := responses[code].(map[string]any)
	if !ok {
		if resp, ok = responses["default"].(map[string]any); !ok || status < 400 {
			return fmt.Sprintf("кода %d нет в договоре", status)
		}
		code = "default"
	}
	// Ответ — ссылкой на общий (components/responses/…): схема — там.
	base := pointer("paths", path, strings.ToLower(method), "responses", code)
	if ref, ok := resp["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/responses/")
		comps, _ := c.doc["components"].(map[string]any)
		all, _ := comps["responses"].(map[string]any)
		if resp, ok = all[name].(map[string]any); !ok {
			return "нет общего ответа " + ref
		}
		base = pointer("components", "responses", name)
	}
	content, _ := resp["content"].(map[string]any)
	if !strings.HasPrefix(contentType, "application/json") || len(bytes.TrimSpace(body)) == 0 {
		if _, wantJSON := content["application/json"]; wantJSON && status != http.StatusNoContent && len(bytes.TrimSpace(body)) == 0 {
			return "договор обещает JSON, а тело пустое"
		}
		return ""
	}
	if _, ok := content["application/json"]; !ok {
		return "" // договор не описывает тело (например, редирект с пояснением)
	}
	ptr := "people.json#" + base + pointer("content", "application/json", "schema")
	c.mu.Lock()
	sch, ok := c.schemas[ptr]
	var err error
	if !ok {
		sch, err = c.compiler.Compile(ptr)
		if err == nil {
			c.schemas[ptr] = sch
		}
	}
	c.mu.Unlock()
	if err != nil {
		return "схема не собирается: " + err.Error()
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return "тело не JSON: " + err.Error()
	}
	if err := sch.Validate(inst); err != nil {
		return "тело не по схеме: " + err.Error()
	}
	return ""
}

type capture struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *capture) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *capture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.body.Write(b)
	return c.ResponseWriter.Write(b)
}

var recordMu sync.Mutex

// contractCheck — прослойка тестового сервера: сверка с договором (и запись ответов при PEOPLE_RECORD).
func contractCheck(t *testing.T, next http.Handler) http.Handler {
	c, err := loadPeopleContract()
	if err != nil {
		t.Fatalf("договор API людей: %v", err)
	}
	record := os.Getenv("PEOPLE_RECORD")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") || r.URL.Path == "/api/v1/ws" {
			next.ServeHTTP(w, r)
			return
		}
		cw := &capture{ResponseWriter: w}
		next.ServeHTTP(cw, r)
		method, path := r.Method, c.match(r.Method, r.URL.Path)
		if path == "" {
			return // шаблона нет в договоре — это ловит TestPeopleContractCoversEveryRoute
		}
		if record != "" {
			recordMu.Lock()
			if f, err := os.OpenFile(record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				line, _ := json.Marshal(map[string]any{"method": method, "path": path, "status": cw.status,
					"type": cw.Header().Get("Content-Type"), "body": cw.body.String()})
				_, _ = f.Write(append(line, '\n'))
				_ = f.Close()
			}
			recordMu.Unlock()
		}
		if record != "" {
			return // запись черновика — без сверки
		}
		if problem := c.check(method, path, cw.status, cw.Header().Get("Content-Type"), cw.body.Bytes()); problem != "" {
			t.Errorf("договор API людей: %s %s → %d: %s\n%s", method, path, cw.status, problem, cw.body.String())
		}
	})
}

// Все адреса /api/v1 из кода — в договоре, и в договоре нет лишних (ADR-80).
func TestPeopleContractCoversEveryRoute(t *testing.T) {
	c, err := loadPeopleContract()
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`HandleFunc\("([A-Z]+) (/api/v1/[^"]*)"`)
	code := map[string]bool{}
	entries, _ := os.ReadDir(".")
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := os.Open(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if m := re.FindStringSubmatch(sc.Text()); m != nil {
				code[m[1]+" "+m[2]] = true
			}
		}
		_ = f.Close()
	}
	described := map[string]bool{}
	paths, _ := c.doc["paths"].(map[string]any)
	for p, item := range paths {
		for m := range item.(map[string]any) {
			if m == "parameters" || m == "summary" || m == "description" {
				continue
			}
			described[strings.ToUpper(m)+" "+p] = true
		}
	}
	var missing, extra []string
	for k := range code {
		if !described[k] {
			missing = append(missing, k)
		}
	}
	for k := range described {
		if !code[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("договор API людей разошёлся с кодом:\nнет в договоре: %v\nнет в коде: %v", missing, extra)
	}
	if v, _ := c.doc["info"].(map[string]any)["version"].(string); v == "" {
		t.Fatal("у договора нет версии info.version")
	}
}

var templateParam = regexp.MustCompile(`\{[^}/]+\}`)

// match — шаблон договора для адреса запроса (/api/v1/facts/123 → /api/v1/facts/{id}); точный адрес главнее шаблона.
func (c *peopleContract) match(method, path string) string {
	paths, _ := c.doc["paths"].(map[string]any)
	best := ""
	for tpl := range paths {
		if c.operation(method, tpl) == nil {
			continue
		}
		re := regexp.MustCompile("^" + templateParam.ReplaceAllString(tpl, `[^/]+`) + "$")
		if re.MatchString(path) && (best == "" || strings.Count(best, "{") > strings.Count(tpl, "{")) {
			best = tpl
		}
	}
	return best
}
