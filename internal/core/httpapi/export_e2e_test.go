package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// exportZip — выгрузка админом; возвращает файлы архива.
func (s *stack) exportZip(token, from, to string, want int) map[string]string {
	s.t.Helper()
	req, _ := http.NewRequest("GET", s.srv.URL+"/api/v1/admin/export?from="+from+"&to="+to, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.srv.Client().Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		s.t.Fatalf("export: %d %s", resp.StatusCode, body)
	}
	if want != http.StatusOK {
		return nil
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		s.t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	return files
}

// ADR-67: вся база за период, без секретов, только админу.
func TestAdminExportOverHTTP(t *testing.T) {
	s := newStack(t)
	user := s.register("user@example.org")
	admin := s.register("admin@example.org")
	if _, err := s.pool.Exec(context.Background(), `UPDATE users SET role = 'admin' WHERE email = 'admin@example.org'`); err != nil {
		t.Fatal(err)
	}
	s.call("POST", "/api/v1/topup-marks", user, nil, http.StatusCreated)
	today := time.Now().UTC().Format(time.DateOnly)
	tomorrow := time.Now().UTC().Add(24 * time.Hour).Format(time.DateOnly)

	s.exportZip(user, today, tomorrow, http.StatusForbidden)
	s.exportZip(admin, tomorrow, today, http.StatusUnprocessableEntity)
	s.exportZip(admin, "вчера", today, http.StatusUnprocessableEntity)

	files := s.exportZip(admin, today, tomorrow, http.StatusOK)
	if !strings.Contains(files["users.jsonl"], "user@example.org") || strings.Count(files["users.jsonl"], "\n") != 2 {
		t.Fatalf("users: %q", files["users.jsonl"])
	}
	all := strings.Join(mapValues(files), "\n")
	for _, secret := range []string{"password_hash", "token_hash", "code_hash", "mark_hash", "$argon2"} {
		if strings.Contains(all, secret) {
			t.Fatalf("secret %q in the export", secret)
		}
	}
	for _, skipped := range []string{"refresh_tokens.jsonl", "topup_marks.jsonl", "oauth_states.jsonl"} {
		if _, ok := files[skipped]; ok {
			t.Fatalf("%s must not be exported", skipped)
		}
	}
	var mf struct {
		Tables map[string]struct {
			Rows     int    `json:"rows"`
			ByPeriod string `json:"by_period"`
		} `json:"tables"`
	}
	if err := json.Unmarshal([]byte(files["manifest.json"]), &mf); err != nil {
		t.Fatal(err)
	}
	if mf.Tables["users"].Rows != 2 || mf.Tables["users"].ByPeriod != "created_at" || mf.Tables["currencies"].ByPeriod != "" {
		t.Fatalf("manifest: %+v", mf.Tables)
	}

	// Прошлый период: людей нет, справочники — целиком.
	old := s.exportZip(admin, "2000-01-01", "2000-01-02", http.StatusOK)
	if old["users.jsonl"] != "" || !strings.Contains(old["currencies.jsonl"], `"GC"`) {
		t.Fatalf("old period: users %q currencies %q", old["users.jsonl"], old["currencies.jsonl"])
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
