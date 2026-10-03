// Package httpapi — HTTP-потоки ядра: клиентский API (5.7), шлюз прорабов (ADR-45)
// и WebSocket (ADR-46). Домен-логики здесь нет — только транспорт и коды ответов.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/auth"
	"offgrid/core/internal/core/codes"
	"offgrid/core/internal/core/engine"
	"offgrid/core/internal/core/files"
	"offgrid/core/internal/core/fsm"
	"offgrid/core/internal/core/realtime"
)

// maxBody — потолок тела запроса.
const maxBody = 1 << 20

// Server — HTTP-обвязка ядра.
type Server struct {
	eng  *engine.Engine
	auth *auth.Service
	hub  *realtime.Hub
	pool *pgxpool.Pool
	log  *slog.Logger

	publicURL string       // CORE_PUBLIC_URL (ADR-58)
	signer    files.Signer // подписанные ссылки на файлы (ADR-60)
	linkTTL   time.Duration
}

// New создаёт сервер.
func New(eng *engine.Engine, a *auth.Service, hub *realtime.Hub, log *slog.Logger) *Server {
	return &Server{eng: eng, auth: a, hub: hub, pool: eng.Pool(), log: log}
}

// Handler — маршруты ядра.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
	})
	mux.HandleFunc("GET /readyz", s.readyz)

	mux.HandleFunc("POST /api/v1/auth/register", s.register)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/refresh", s.refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/auth/providers", s.authProviders)
	mux.HandleFunc("GET /api/v1/auth/oauth/{provider}/start", s.oauthStart)
	mux.HandleFunc("GET /api/v1/auth/oauth/{provider}/callback", s.oauthCallback)
	mux.HandleFunc("POST /api/v1/auth/oauth/exchange", s.oauthExchange)
	mux.HandleFunc("GET /api/v1/me", s.user(s.me))
	mux.HandleFunc("PATCH /api/v1/me", s.user(s.updateMe))
	mux.HandleFunc("POST /api/v1/me/password", s.user(s.changePassword))
	mux.HandleFunc("GET /api/v1/me/identities", s.user(s.identities))
	mux.HandleFunc("POST /api/v1/me/identities/{provider}/link", s.user(s.oauthLink))
	mux.HandleFunc("DELETE /api/v1/me/identities/{provider}/{subject}", s.user(s.unlink))
	mux.HandleFunc("GET /api/v1/features", s.features)

	mux.HandleFunc("POST /api/v1/files", s.user(s.uploadFile))
	mux.HandleFunc("GET /api/v1/files/{id}", s.user(s.getFile))
	mux.HandleFunc("DELETE /api/v1/files/{id}", s.user(s.deleteFile))
	mux.HandleFunc("GET /api/v1/files/{id}/content", s.fileContent)

	mux.HandleFunc("GET /api/v1/catalog", s.catalog)
	mux.HandleFunc("GET /api/v1/forms/{form_id}", s.template)

	mux.HandleFunc("GET /api/v1/wallets", s.user(s.wallets))
	mux.HandleFunc("GET /api/v1/wallets/transactions", s.user(s.transactions))
	mux.HandleFunc("POST /api/v1/deposits/demo", s.user(s.demoDeposit))
	mux.HandleFunc("POST /api/v1/promo-codes/redeem", s.user(s.redeemPromo))

	mux.HandleFunc("POST /api/v1/facts", s.user(s.createFact))
	mux.HandleFunc("GET /api/v1/facts", s.user(s.listFacts))
	mux.HandleFunc("GET /api/v1/facts/{id}", s.user(s.getFact))
	mux.HandleFunc("PUT /api/v1/facts/{id}", s.user(s.updateFact))
	mux.HandleFunc("POST /api/v1/facts/{id}/publish", s.user(s.publishFact))
	mux.HandleFunc("DELETE /api/v1/facts/{id}", s.user(s.deleteFact))

	mux.HandleFunc("POST /api/v1/tasks", s.user(s.createTask))
	mux.HandleFunc("GET /api/v1/tasks", s.user(s.listTasks))
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.user(s.getTask))
	mux.HandleFunc("POST /api/v1/tasks/{id}/commands", s.user(s.command))

	mux.HandleFunc("GET /api/v1/ws", s.user(func(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
		s.hub.Serve(w, r, u)
	}))

	mux.HandleFunc("POST /gateway/v1/claim", s.prorab(s.claim))
	mux.HandleFunc("POST /gateway/v1/tasks/{id}/accept", s.prorab(s.accept))
	mux.HandleFunc("POST /gateway/v1/tasks/{id}/heartbeat", s.prorab(s.heartbeat))
	mux.HandleFunc("POST /gateway/v1/tasks/{id}/progress", s.prorab(s.progress))
	mux.HandleFunc("POST /gateway/v1/tasks/{id}/commit", s.prorab(s.commit))
	mux.HandleFunc("GET /gateway/v1/tasks/{id}/files/{file_id}", s.prorab(s.taskFile))

	// Эмитенты промокодов — службы оплаты (ADR-57), contracts/interfaces/promo_issuer.openapi.yaml.
	mux.HandleFunc("POST /issuer/v1/promo-codes", s.issuer(s.issuePromo))
	mux.HandleFunc("GET /issuer/v1/promo-codes/{id}", s.issuer(s.promoStatus))
	mux.HandleFunc("POST /issuer/v1/promo-codes/{id}/revoke", s.issuer(s.revokePromo))
	mux.HandleFunc("POST /issuer/v1/redeem", s.issuer(s.issuerRedeem))
	return mux
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// --- аутентификация -------------------------------------------------------

type userHandler func(w http.ResponseWriter, r *http.Request, userID uuid.UUID)

// user — access-JWT из заголовка Authorization; для WebSocket — из ?access_token
// (браузер не умеет ставить заголовки на WS).
func (s *Server) user(h userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" && r.URL.Path == "/api/v1/ws" {
			token = r.URL.Query().Get("access_token")
		}
		id, err := s.auth.ParseAccess(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid access token required")
			return
		}
		h(w, r, id)
	}
}

type prorabHandler func(w http.ResponseWriter, r *http.Request, acc engine.ServiceAccount)

func (s *Server) prorab(h prorabHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acc, err := s.eng.AuthenticateService(r.Context(), bearer(r))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid service token required")
			return
		}
		h(w, r, acc)
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if !decode(w, r, &req) {
		return
	}
	id, tokens, err := s.auth.Register(r.Context(), req.Email, req.Password, req.DisplayName)
	switch {
	case errors.Is(err, auth.ErrWeakInput):
		writeError(w, http.StatusUnprocessableEntity, "invalid_input", err.Error())
	case errors.Is(err, auth.ErrEmailTaken):
		writeError(w, http.StatusConflict, "email_taken", err.Error())
	case err != nil:
		s.internal(w, err)
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"user_id": id, "tokens": tokens})
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	id, tokens, err := s.auth.Login(r.Context(), req.Email, req.Password)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", err.Error())
	case err != nil:
		s.internal(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"user_id": id, "tokens": tokens})
	}
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decode(w, r, &req) {
		return
	}
	tokens, err := s.auth.Refresh(r.Context(), req.RefreshToken)
	switch {
	case errors.Is(err, auth.ErrInvalidToken):
		writeError(w, http.StatusUnauthorized, "invalid_token", err.Error())
	case err != nil:
		s.internal(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.auth.Logout(r.Context(), req.RefreshToken); err != nil {
		s.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	p, err := s.auth.Profile(r.Context(), u)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// --- каталог и кошелёк ----------------------------------------------------

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	items, err := s.eng.Catalog(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) template(w http.ResponseWriter, r *http.Request) {
	t, err := s.eng.Template(r.Context(), r.PathValue("form_id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) wallets(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	ws, err := s.eng.Wallets(r.Context(), u)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ws})
}

func (s *Server) transactions(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	limit, _ := paging(r)
	items, err := s.eng.Transactions(r.Context(), u, limit)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) demoDeposit(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	var req struct {
		Amount decimal.Decimal `json:"amount"`
	}
	if !decode(w, r, &req) {
		return
	}
	view, err := s.eng.DemoDeposit(r.Context(), u, req.Amount)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// --- факты ----------------------------------------------------------------

func (s *Server) createFact(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	var req struct {
		FormID string          `json:"form_id"`
		Values json.RawMessage `json:"values"`
	}
	if !decode(w, r, &req) {
		return
	}
	f, err := s.eng.CreateFact(r.Context(), u, req.FormID, req.Values)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) listFacts(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	// ?kind=…&fields=title,language (поля опубликованной версии в summary)&live=true (без удалённых)
	q := r.URL.Query()
	opts := engine.FactListOptions{Kind: q.Get("kind"), Live: q.Get("live") == "true"}
	for _, f := range strings.Split(q.Get("fields"), ",") {
		if f = strings.TrimSpace(f); f != "" && len(opts.Fields) < 20 {
			opts.Fields = append(opts.Fields, f)
		}
	}
	items, err := s.eng.ListFacts(r.Context(), u, opts)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getFact(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	f, err := s.eng.GetFact(r.Context(), u, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) updateFact(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Values json.RawMessage `json:"values"`
	}
	if !decode(w, r, &req) {
		return
	}
	f, err := s.eng.UpdateFact(r.Context(), u, id, req.Values)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) publishFact(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
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
	f, err := s.eng.PublishFact(r.Context(), u, id, req.Version)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) deleteFact(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.eng.DeleteFact(r.Context(), u, id); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- задачи ---------------------------------------------------------------

func (s *Server) createTask(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	var req engine.CreateTaskRequest
	if !decode(w, r, &req) {
		return
	}
	env, created, err := s.eng.CreateTask(r.Context(), u, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	status := http.StatusOK // повтор с тем же ключом — существующая задача
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, env)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	limit, offset := paging(r)
	status := r.URL.Query().Get("status")
	if status != "" && !knownStatus(status) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_input", "unknown status")
		return
	}
	page, err := s.eng.ListTasks(r.Context(), u, status, limit, offset)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	env, err := s.eng.GetTask(r.Context(), u, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, env)
}

func (s *Server) command(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Command        fsm.Command `json:"command"`
		IdempotencyKey string      `json:"idempotency_key"`
	}
	if !decode(w, r, &req) {
		return
	}
	env, err := s.eng.Command(r.Context(), u, id, req.Command, req.IdempotencyKey)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, env)
}

// --- шлюз прорабов --------------------------------------------------------

func (s *Server) claim(w http.ResponseWriter, r *http.Request, acc engine.ServiceAccount) {
	var req struct {
		TargetService  string `json:"target_service"`
		SchemaVersions []int  `json:"schema_versions"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.TargetService != acc.ServiceCode {
		writeError(w, http.StatusForbidden, "forbidden", "prorab serves only its own target_service")
		return
	}
	task, err := s.eng.Claim(r.Context(), acc, req.SchemaVersions)
	if err != nil {
		s.fail(w, err)
		return
	}
	if task == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) accept(w http.ResponseWriter, r *http.Request, acc engine.ServiceAccount) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Accepted   bool `json:"accepted"`
		ETASeconds int  `json:"eta_seconds"`
	}
	if !decode(w, r, &req) {
		return
	}
	state, err := s.eng.Accept(r.Context(), acc, id, req.Accepted, req.ETASeconds)
	s.gatewayReply(w, state, err)
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request, acc engine.ServiceAccount) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	state, err := s.eng.Heartbeat(r.Context(), acc, id)
	s.gatewayReply(w, state, err)
}

func (s *Server) progress(w http.ResponseWriter, r *http.Request, acc engine.ServiceAccount) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Percent int    `json:"percent"`
		Phase   string `json:"phase"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.eng.Progress(r.Context(), acc, id, req.Percent, req.Phase); err != nil {
		if errors.Is(err, engine.ErrNotOwner) {
			writeError(w, http.StatusConflict, "not_owner", err.Error())
			return
		}
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) commit(w http.ResponseWriter, r *http.Request, acc engine.ServiceAccount) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req engine.CommitRequest
	if !decode(w, r, &req) {
		return
	}
	state, err := s.eng.Commit(r.Context(), acc, id, req)
	s.gatewayReply(w, state, err)
}

func (s *Server) gatewayReply(w http.ResponseWriter, state engine.TaskState, err error) {
	switch {
	case errors.Is(err, engine.ErrNotOwner):
		writeJSON(w, http.StatusConflict, state)
	case err != nil:
		s.fail(w, err)
	default:
		writeJSON(w, http.StatusOK, state)
	}
}

// --- общие помощники ------------------------------------------------------

// fail переводит ошибку ядра в HTTP: отказ с кодом реестра — report в теле.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var refusal *engine.Refusal
	switch {
	case errors.As(err, &refusal):
		writeJSON(w, refusalStatus(refusal.Code), map[string]any{"report": refusal.Report()})
	case errors.Is(err, engine.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, auth.ErrInvalidToken):
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid token")
	default:
		s.internal(w, err)
	}
}

func refusalStatus(c codes.Code) int {
	switch c {
	case codes.InsufficientFunds:
		return http.StatusPaymentRequired
	case codes.CommandRefused:
		return http.StatusConflict
	default:
		return http.StatusUnprocessableEntity
	}
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	s.log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return id, false
	}
	return id, true
}

func paging(r *http.Request) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func knownStatus(s string) bool {
	for _, st := range fsm.Statuses {
		if string(st) == s {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
