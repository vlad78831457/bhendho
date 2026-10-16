package httpapi

// Подразделения подключаются сами (ADR-70): регистрация по ключу, ключи и выключатель — у админа.

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"offgrid/core/internal/core/engine"
)

// registerDepartment — POST /gateway/v1/register с ключом подразделения:
// {"module_yaml": "...", "files": {"forms/start.json": "{...}"}}.
func (s *Server) registerDepartment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ModuleYAML string            `json:"module_yaml"`
		Files      map[string]string `json:"files"`
		Prorab     engine.ProrabInfo `json:"prorab"` // ADR-77: название и описание прораба
	}
	if !decode(w, r, &req) {
		return
	}
	files := make(map[string][]byte, len(req.Files))
	for k, v := range req.Files {
		files[k] = []byte(v)
	}
	out, err := s.eng.RegisterDepartment(r.Context(), bearer(r), []byte(req.ModuleYAML), files, req.Prorab)
	switch {
	case errors.Is(err, engine.ErrNotFound):
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid department key required")
	case errors.Is(err, engine.ErrForeignDepartment):
		writeError(w, http.StatusForbidden, "foreign_department", err.Error())
	case err != nil:
		s.fail(w, err)
	default:
		writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) adminDepartmentKey(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	var req struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name == "" {
		req.Name = "main"
	}
	token, err := s.eng.IssueDepartmentKey(r.Context(), u, req.Code, req.Name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"code": req.Code, "name": req.Name, "key": token})
}

func (s *Server) adminSetService(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_input", "enabled is required")
		return
	}
	if err := s.eng.SetServiceEnabled(r.Context(), r.PathValue("service"), *req.Enabled); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// views — разметка общей территории фронта (ADR-72); открыта, как и каталог.
func (s *Server) views(w http.ResponseWriter, r *http.Request) {
	items, err := s.eng.Views(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) adminProrabs(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	items, err := s.eng.ProrabPriorities(r.Context(), r.PathValue("service"))
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// adminSetProrab — {prorab, form_id, priority}: уровень прораба на бланк (ADR-76).
func (s *Server) adminSetProrab(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	var req struct {
		Prorab   string `json:"prorab"`
		FormID   string `json:"form_id"`
		Priority int    `json:"priority"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.eng.SetProrabPriority(r.Context(), u, r.PathValue("service"), req.Prorab, req.FormID, req.Priority); err != nil {
		s.fail(w, err)
		return
	}
	s.adminProrabs(w, r, u)
}

func (s *Server) adminDepartmentKeys(w http.ResponseWriter, r *http.Request, _ uuid.UUID) {
	items, err := s.eng.DepartmentKeys(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
