package httpapi

// Витрина (ADR-74): админ ведёт её в панели, посадочная страница читает открыто.

import (
	"net/http"

	"github.com/google/uuid"
)

// showcase — открытая витрина языка ?lang= (нет мест на нём — язык по умолчанию).
func (s *Server) showcase(w http.ResponseWriter, r *http.Request) {
	items, err := s.eng.PublicShowcase(r.Context(), r.URL.Query().Get("lang"), s.eng.ShowcaseFallback())
	if err != nil {
		s.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) adminShowcase(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	items, err := s.eng.AdminShowcase(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) adminAddShowcase(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	var req struct {
		FactID   uuid.UUID `json:"fact_id"`
		Language string    `json:"language"`
		Position int       `json:"position"`
	}
	if !decode(w, r, &req) {
		return
	}
	it, err := s.eng.AddToShowcase(r.Context(), u, req.FactID, req.Language, req.Position)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, it)
}

func (s *Server) adminRemoveShowcase(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.eng.RemoveFromShowcase(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
