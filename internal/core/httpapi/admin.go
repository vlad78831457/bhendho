package httpapi

// Панель админа (ADR-66): только роль admin (ADR-61). Ядро не понимает фактов подразделений —
// показывает, принимает от админа и отдаёт как есть.

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

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

// adminExport — вся база за период архивом (ADR-67): ?from=2026-10-01&to=2026-10-16 (to не включается).
func (s *Server) adminExport(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	from, err1 := time.Parse(time.DateOnly, r.URL.Query().Get("from"))
	to, err2 := time.Parse(time.DateOnly, r.URL.Query().Get("to"))
	if err1 != nil || err2 != nil || !from.Before(to) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_input", "from and to are dates YYYY-MM-DD, from before to")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="offgrid-%s-%s.zip"`, from.Format(time.DateOnly), to.Format(time.DateOnly)))
	if err := s.eng.ExportDB(r.Context(), w, from, to); err != nil {
		s.log.Error("export", "err", err) // заголовки уже ушли — архив оборван, клиент увидит битый файл
	}
}
