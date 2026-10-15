package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// ADR-73: ссылка на версию факта — без входа, только чтение, отзывается; копия к себе — без чужих файлов.
func TestShareOverHTTP(t *testing.T) {
	s := newStack(t)
	owner := s.register("owner@example.org")
	friend := s.register("friend@example.org")

	photo := s.upload(owner, pngBytes(t, 40, 30), http.StatusCreated)["file"].(map[string]any)["file_id"].(string)
	hero := s.call("POST", "/api/v1/facts", owner, map[string]any{"form_id": "taleweaver.character.v1",
		"values": map[string]any{"name": "Лисёнок", "photo": photo}}, http.StatusCreated)["fact_id"].(string)

	// Черновиком не делятся; чужим фактом — тоже.
	s.call("POST", "/api/v1/facts/"+hero+"/shares", owner, nil, http.StatusNotFound)
	s.call("POST", "/api/v1/facts/"+hero+"/publish", owner, nil, http.StatusOK)
	s.call("POST", "/api/v1/facts/"+hero+"/shares", friend, nil, http.StatusNotFound)

	share := s.call("POST", "/api/v1/facts/"+hero+"/shares", owner, nil, http.StatusCreated)
	token := share["token"].(string)
	if !strings.HasPrefix(token, "sh_") || share["path"] != "/s/"+token {
		t.Fatalf("share: %v", share)
	}

	// Без входа: значения, название бланка, фото по подписанной ссылке; поисковикам — нет.
	code, hdr, _ := s.raw("GET", "/api/v1/shared/"+token, "", "", nil)
	if code != http.StatusOK || !strings.Contains(hdr.Get("X-Robots-Tag"), "noindex") {
		t.Fatalf("shared: %d %v", code, hdr)
	}
	view := s.call("GET", "/api/v1/shared/"+token, "", nil, http.StatusOK)
	link := view["files"].(map[string]any)["photo"].(map[string]any)["url"].(string)
	if view["values"].(map[string]any)["name"] != "Лисёнок" || view["kind"] != "taleweaver.character.v1" || view["title"] == "" {
		t.Fatalf("shared view: %v", view)
	}
	if code, _, _ := s.raw("GET", link, "", "", nil); code != http.StatusOK {
		t.Fatalf("photo by signed link: %d", code)
	}

	// Копия к себе: новый факт друга, без фото хозяина; оригинал не тронут.
	s.call("POST", "/api/v1/shared/"+token+"/copy", "", nil, http.StatusUnauthorized)
	copied := s.call("POST", "/api/v1/shared/"+token+"/copy", friend, nil, http.StatusCreated)
	values := copied["versions"].([]any)[0].(map[string]any)["values"].(map[string]any)
	if copied["fact_id"] == hero || values["name"] != "Лисёнок" || values["photo"] != nil {
		t.Fatalf("copy: %v", copied)
	}
	if v := s.call("GET", "/api/v1/facts/"+hero, owner, nil, http.StatusOK)["versions"].([]any)[0].(map[string]any)["values"].(map[string]any); v["photo"] != photo {
		t.Fatalf("original changed: %v", v)
	}

	// Ссылки видит и отзывает только хозяин; отозванная — как несуществующая.
	if items := s.call("GET", "/api/v1/facts/"+hero+"/shares", owner, nil, http.StatusOK)["items"].([]any); len(items) != 1 {
		t.Fatalf("shares: %v", items)
	}
	id := share["share_id"].(string)
	s.call("DELETE", "/api/v1/shares/"+id, friend, nil, http.StatusNotFound)
	s.call("DELETE", "/api/v1/shares/"+id, owner, nil, http.StatusNoContent)
	s.call("GET", "/api/v1/shared/"+token, "", nil, http.StatusNotFound)
	s.call("POST", "/api/v1/shared/"+token+"/copy", friend, nil, http.StatusNotFound)
	s.call("GET", "/api/v1/shared/sh_nope", "", nil, http.StatusNotFound)
}
