package httpapi

// Файлы пользователей (ADR-60): загрузка картинки, подписанная ссылка для <img>, удаление;
// прораб скачивает файл своей задачи через шлюз.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"offgrid/core/internal/core/engine"
	"offgrid/core/internal/core/files"
)

// WithFileLinks — ключ подписанных ссылок и их срок.
func (s *Server) WithFileLinks(signer files.Signer, ttl time.Duration) *Server {
	s.signer, s.linkTTL = signer, ttl
	return s
}

func (s *Server) fileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrFilesDisabled):
		writeError(w, http.StatusServiceUnavailable, "files_disabled", "file storage is not configured")
	case errors.Is(err, files.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", err.Error())
	case errors.Is(err, files.ErrUnsupported):
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_file", err.Error())
	case errors.Is(err, engine.ErrFileQuota):
		writeError(w, http.StatusConflict, "file_quota", err.Error())
	default:
		s.fail(w, err)
	}
}

// fileLink — адрес содержимого с подписью: <img src> работает без токена, пока не вышел срок.
func (s *Server) fileLink(id uuid.UUID) map[string]any {
	if s.linkTTL <= 0 { // ключ ссылок не задан — ссылок не выдаём
		return nil
	}
	until := time.Now().Add(s.linkTTL)
	exp, sig := s.signer.Sign(id.String(), until)
	return map[string]any{
		"url":        fmt.Sprintf("/api/v1/files/%s/content?exp=%d&sig=%s", id, exp, sig),
		"expires_at": until.UTC().Format(time.RFC3339),
	}
}

// uploadFile — тело запроса — сама картинка (как есть, не multipart); имя — ?filename=.
func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	v, err := s.eng.UploadImage(r.Context(), u, r.URL.Query().Get("filename"), r.Body)
	if err != nil {
		s.fileError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"file": v, "link": s.fileLink(v.ID)})
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.eng.FileOf(r.Context(), u, id)
	if err != nil {
		s.fileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"file": v, "link": s.fileLink(v.ID)})
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.eng.DeleteFile(r.Context(), u, id); err != nil {
		s.fileError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fileContent — по подписанной ссылке, без токена. Ошибка подписи и отсутствие файла
// неотличимы (404): по ответу не узнать, существует ли чужой файл.
func (s *Server) fileContent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	if s.linkTTL <= 0 || !s.signer.Valid(id.String(), q.Get("exp"), q.Get("sig"), time.Now()) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	v, rc, err := s.eng.OpenFile(r.Context(), id)
	if err != nil {
		s.fileError(w, err)
		return
	}
	defer rc.Close()
	exp, _ := strconv.ParseInt(q.Get("exp"), 10, 64)
	serveFile(w, v, rc, max(0, exp-time.Now().Unix()))
}

// taskFile — прораб скачивает файл из снимка своей задачи.
func (s *Server) taskFile(w http.ResponseWriter, r *http.Request, acc engine.ServiceAccount) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	fileID, err := uuid.Parse(r.PathValue("file_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	v, rc, err := s.eng.TaskFile(r.Context(), acc, id, fileID)
	if errors.Is(err, engine.ErrNotOwner) {
		writeError(w, http.StatusConflict, "not_owner", err.Error())
		return
	}
	if err != nil {
		s.fileError(w, err)
		return
	}
	defer rc.Close()
	serveFile(w, v, rc, 0)
}

func serveFile(w http.ResponseWriter, v engine.FileView, rc io.Reader, maxAge int64) {
	h := w.Header()
	h.Set("Content-Type", v.Mime)
	h.Set("Content-Length", strconv.FormatInt(v.Size, 10))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(v.Filename))
	if maxAge > 0 {
		h.Set("Cache-Control", "private, max-age="+strconv.FormatInt(maxAge, 10))
	} else {
		h.Set("Cache-Control", "no-store")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}
