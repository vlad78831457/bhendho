package httpapi

// «Поделиться» (ADR-73): ссылка на версию факта, только чтение; копия к себе.

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
)

func (s *Server) createShare(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Version int `json:"version"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	sh, token, err := s.eng.CreateShare(r.Context(), u, id, req.Version)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"share_id": sh.ID, "fact_id": sh.FactID, "version": sh.Version, "created_at": sh.CreatedAt,
		"token": token, "path": "/s/" + token, // фронт строит из пути свою страницу просмотра
	})
}

func (s *Server) listShares(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	items, err := s.eng.Shares(r.Context(), u, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.eng.RevokeShare(r.Context(), u, id); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// shared — факт по ссылке без входа. Поисковикам не показывается; файлы — по подписанным ссылкам на чтение.
func (s *Server) shared(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "no-store")
	sf, err := s.eng.Shared(r.Context(), r.PathValue("token"))
	if err != nil {
		s.fail(w, err)
		return
	}
	files := map[string]any{}
	for _, field := range sf.FileFields {
		if id, err := uuid.Parse(strings.TrimSpace(asString(sf.Values[field]))); err == nil {
			if link := s.fileLink(id); link != nil {
				files[field] = link
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kind": sf.Kind, "title": sf.Title, "version": sf.Version, "created_at": sf.CreatedAt, "values": sf.Values, "files": files,
	})
}

func (s *Server) copyShared(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	f, err := s.eng.CopyShared(r.Context(), u, r.PathValue("token"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
