package httpapi

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{G: 200, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// raw — запрос с телом как есть; ответ: статус, заголовки, тело.
func (s *stack) raw(method, path, token, contentType string, body []byte) (int, http.Header, []byte) {
	s.t.Helper()
	req, _ := http.NewRequest(method, s.srv.URL+path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, data
}

func (s *stack) upload(token string, body []byte, want int) map[string]any {
	s.t.Helper()
	code, _, data := s.raw("POST", "/api/v1/files?filename="+"..%2F..%2Fмой%20сын.png", token, "image/png", body)
	if code != want {
		s.t.Fatalf("upload: %d, want %d: %s", code, want, data)
	}
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return out
}

func TestFilesOverHTTP(t *testing.T) {
	s := newStack(t)
	if f := s.call("GET", "/api/v1/features", "", nil, http.StatusOK); f["files"] != true {
		t.Fatalf("features: %v", f)
	}
	mom := s.register("mom@example.org")
	stranger := s.register("stranger@example.org")

	s.upload("", pngBytes(t, 10, 10), http.StatusUnauthorized)
	s.upload(mom, []byte("<svg onload=alert(1)>"), http.StatusUnsupportedMediaType)

	up := s.upload(mom, pngBytes(t, 3000, 1500), http.StatusCreated)
	file := up["file"].(map[string]any)
	fileID := file["file_id"].(string)
	if file["mime"] != "image/jpeg" || file["width"] != float64(1024) || file["height"] != float64(512) || file["filename"] != "мой сын.jpg" {
		t.Fatalf("stored file: %v", file)
	}

	// Показ: подписанная ссылка без токена; чужому пользователю файл не виден.
	link := s.call("GET", "/api/v1/files/"+fileID, mom, nil, http.StatusOK)["link"].(map[string]any)["url"].(string)
	code, hdr, body := s.raw("GET", link, "", "", nil)
	if code != http.StatusOK || hdr.Get("Content-Type") != "image/jpeg" || hdr.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(hdr.Get("Cache-Control"), "private") || len(body) == 0 {
		t.Fatalf("content: %d %v", code, hdr)
	}
	if code, _, _ := s.raw("GET", strings.Replace(link, "sig=", "sig=x", 1), "", "", nil); code != http.StatusNotFound {
		t.Fatalf("tampered link: %d", code)
	}
	if code, _, _ := s.raw("GET", "/api/v1/files/"+fileID+"/content", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("unsigned: %d", code)
	}
	s.call("GET", "/api/v1/files/"+fileID, stranger, nil, http.StatusNotFound)
	s.call("DELETE", "/api/v1/files/"+fileID, stranger, nil, http.StatusNotFound)

	// Фото чужого человека в своего героя не вставить.
	s.call("POST", "/api/v1/facts", stranger, map[string]any{"form_id": "taleweaver.character.v1",
		"values": map[string]any{"name": "Чужой", "photo": fileID}}, http.StatusUnprocessableEntity)

	// Герой с фото → сказка → прораб видит фото в снимке и скачивает его по своей задаче.
	hero := s.call("POST", "/api/v1/facts", mom, map[string]any{"form_id": "taleweaver.character.v1",
		"values": map[string]any{"name": "Миша", "photo": fileID}}, http.StatusCreated)
	heroID := hero["fact_id"].(string)
	s.call("POST", "/api/v1/facts/"+heroID+"/publish", mom, nil, http.StatusOK)
	s.call("POST", "/api/v1/deposits/demo", mom, map[string]any{"amount": 1000}, http.StatusOK)
	task := s.call("POST", "/api/v1/tasks", mom, map[string]any{"form_id": "taleweaver.generate_start.v1",
		"idempotency_key": "photo-tale", "values": map[string]any{"hero": heroID}}, http.StatusCreated)
	taskID := task["system"].(map[string]any)["document_id"].(string)

	other := s.upload(stranger, pngBytes(t, 20, 20), http.StatusCreated)["file"].(map[string]any)["file_id"].(string)
	// До claim задача не у прораба в работе.
	if code, _, _ := s.raw("GET", "/gateway/v1/tasks/"+taskID+"/files/"+fileID, s.prorab, "", nil); code != http.StatusConflict {
		t.Fatalf("before claim: %d", code)
	}

	claimed := s.call("POST", "/gateway/v1/claim", s.prorab, map[string]any{"target_service": "taleweaver", "schema_versions": []int{1}}, http.StatusOK)
	photo := claimed["blank"].(map[string]any)["values"].(map[string]any)["hero"].(map[string]any)["values"].(map[string]any)["photo"].(map[string]any)
	if photo["file_id"] != fileID || photo["mime"] != "image/jpeg" {
		t.Fatalf("photo in the snapshot: %v", photo)
	}
	code, hdr, got := s.raw("GET", "/gateway/v1/tasks/"+taskID+"/files/"+fileID, s.prorab, "", nil)
	if code != http.StatusOK || !bytes.Equal(got, body) || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("gateway download: %d", code)
	}
	if code, _, _ := s.raw("GET", "/gateway/v1/tasks/"+taskID+"/files/"+other, s.prorab, "", nil); code != http.StatusNotFound {
		t.Fatalf("file outside the task: %d", code)
	}
	if code, _, _ := s.raw("GET", "/gateway/v1/tasks/"+taskID+"/files/"+fileID, "", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}

	// Удаление стирает байты: ни ссылка, ни шлюз их больше не отдают.
	s.call("DELETE", "/api/v1/files/"+fileID, mom, nil, http.StatusNoContent)
	if code, _, _ := s.raw("GET", link, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("content after delete: %d", code)
	}
	if code, _, _ := s.raw("GET", "/gateway/v1/tasks/"+taskID+"/files/"+fileID, s.prorab, "", nil); code != http.StatusNotFound {
		t.Fatalf("gateway after delete: %d", code)
	}
	s.call("DELETE", "/api/v1/files/"+fileID, mom, nil, http.StatusNotFound)
}
