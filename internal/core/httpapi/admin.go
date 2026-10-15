package httpapi

// Панель админа (ADR-66): только роль admin (ADR-61). Ядро не понимает фактов подразделений —
// показывает, принимает от админа и отдаёт как есть.

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"offgrid/core/internal/core/auth"
)

// admin — как user, но пускает только роль admin; остальным 403.
func (s *Server) admin(h userHandler) http.HandlerFunc {
	return s.user(func(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
		role, err := s.auth.Role(r.Context(), u)
		if err != nil || role != auth.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "admin role required")
			return
		}
		h(w, r, u)
	})
}

func (s *Server) adminServices(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	items, err := s.eng.AdminServices(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) adminFacts(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	formID := r.URL.Query().Get("form_id")
	if formID == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_input", "form_id is required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.eng.LatestServiceFacts(r.Context(), r.PathValue("service"), formID, limit)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) adminWriteFact(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	var req struct {
		FormID string         `json:"form_id"`
		Values map[string]any `json:"values"`
	}
	if !decode(w, r, &req) {
		return
	}
	f, err := s.eng.WriteServiceFact(r.Context(), u, r.PathValue("service"), req.FormID, req.Values)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) adminSettings(w http.ResponseWriter, _ *http.Request, _ uuid.UUID) {
	writeJSON(w, http.StatusOK, s.eng.PublicSettings())
}
