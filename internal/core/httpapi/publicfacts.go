package httpapi

// Публичные факты (ADR-79): админ меняет видимость своего факта; вошедший человек читает публичные факты
// вида — например, пресеты студии для полки — и ссылается на них в задачах без копии.

import (
	"net/http"

	"github.com/google/uuid"
)

func (s *Server) publicFacts(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	items, err := s.eng.PublicFacts(r.Context(), r.URL.Query().Get("kind"))
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) adminSetVisibility(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Visibility string `json:"visibility"`
	}
	if !decode(w, r, &req) {
		return
	}
	f, err := s.eng.SetFactVisibility(r.Context(), u, id, req.Visibility)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}
