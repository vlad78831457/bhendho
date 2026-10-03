// Package realtime — WebSocket-доставка событий пользователю (ADR-23, ADR-46).
// Каждый инстанс слушает NOTIFY и отдаёт события своим сокетам; тела событий
// читаются из outbox_queue по seq, поэтому переподключение и дубли безопасны.
package realtime

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"offgrid/core/internal/core/outbox"
)

// ProgressChannel — канал NOTIFY эфемерного прогресса.
const ProgressChannel = "offgrid_progress"

// catchUpBatch — сколько событий читается за один запрос догонки.
const catchUpBatch = 200

// eventNS — пространство имён для стабильного event_id из id строки outbox.
var eventNS = uuid.MustParse("5c0b6f1e-6f8f-4d0e-9d7b-2f1d3f0a9e11")

// Event — событие на проводе (contracts/events/realtime_events.schema.json).
type Event struct {
	EventID    string          `json:"event_id"`
	Type       string          `json:"type"`
	Seq        int64           `json:"seq"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload"`
}

// Hub — подключённые клиенты этого инстанса.
type Hub struct {
	pool      *pgxpool.Pool
	log       *slog.Logger
	ping      time.Duration
	origins   []string
	mu        sync.Mutex
	clients   map[uuid.UUID]map[*client]struct{}
	listening chan struct{}
}

// NewHub создаёт хаб. origins — разрешённые Origin для браузеров (пусто — только same-origin).
func NewHub(pool *pgxpool.Pool, ping time.Duration, origins []string, log *slog.Logger) *Hub {
	return &Hub{pool: pool, log: log, ping: ping, origins: origins,
		clients: map[uuid.UUID]map[*client]struct{}{}, listening: make(chan struct{})}
}

type client struct {
	userID   uuid.UUID
	conn     *websocket.Conn
	wake     chan struct{}
	progress chan []byte
}

func (c *client) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Progress реализует engine.Notifier: эфемерный task.progress через NOTIFY всем инстансам.
// seq события — последний применённый у пользователя (прогресс не нумеруется, ADR-23).
func (h *Hub) Progress(ctx context.Context, userID, taskID uuid.UUID, percent int, phase string) error {
	payload, _ := json.Marshal(map[string]any{"task_id": taskID.String(), "percent": percent, "phase": phase})
	var seq int64
	if err := h.pool.QueryRow(ctx, `SELECT COALESCE((SELECT last_seq FROM user_seq WHERE user_id = $1), 0)`, userID).Scan(&seq); err != nil {
		return err
	}
	ev, _ := json.Marshal(Event{EventID: uuid.NewString(), Type: "task.progress", Seq: seq,
		OccurredAt: time.Now().UTC(), Payload: payload})
	msg, _ := json.Marshal(map[string]any{"u": userID.String(), "e": json.RawMessage(ev)})
	_, err := h.pool.Exec(ctx, `SELECT pg_notify($1, $2)`, ProgressChannel, string(msg))
	return err
}

// Listen держит выделенное соединение с LISTEN и переподключается с растущей паузой.
func (h *Hub) Listen(ctx context.Context) {
	backoff := 100 * time.Millisecond
	first := true
	for ctx.Err() == nil {
		err := h.listenOnce(ctx, func() {
			if first {
				close(h.listening)
				first = false
			}
			backoff = 100 * time.Millisecond
			h.pokeAll() // после переподключения все клиенты догоняют пропущенное
		})
		if ctx.Err() != nil {
			return
		}
		h.log.Warn("realtime listener reconnecting", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

// Ready закрывается, когда LISTEN впервые установлен.
func (h *Hub) Ready() <-chan struct{} { return h.listening }

func (h *Hub) listenOnce(ctx context.Context, onReady func()) error {
	conn, err := pgx.ConnectConfig(ctx, h.pool.Config().ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	for _, ch := range []string{outbox.Channel, ProgressChannel} {
		if _, err := conn.Exec(ctx, "LISTEN "+ch); err != nil {
			return err
		}
	}
	onReady()
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		switch n.Channel {
		case outbox.Channel:
			var msg struct {
				U uuid.UUID `json:"u"`
			}
			if json.Unmarshal([]byte(n.Payload), &msg) == nil {
				h.forUser(msg.U, (*client).poke)
			}
		case ProgressChannel:
			var msg struct {
				U uuid.UUID       `json:"u"`
				E json.RawMessage `json:"e"`
			}
			if json.Unmarshal([]byte(n.Payload), &msg) == nil {
				h.forUser(msg.U, func(c *client) {
					select {
					case c.progress <- msg.E:
					default: // прогресс эфемерен — пропуск безопасен
					}
				})
			}
		}
	}
}

func (h *Hub) forUser(u uuid.UUID, f func(*client)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients[u] {
		f(c)
	}
}

func (h *Hub) pokeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.clients {
		for c := range set {
			c.poke()
		}
	}
}

func (h *Hub) add(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c.userID] == nil {
		h.clients[c.userID] = map[*client]struct{}{}
	}
	h.clients[c.userID][c] = struct{}{}
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients[c.userID], c)
	if len(h.clients[c.userID]) == 0 {
		delete(h.clients, c.userID)
	}
}

// Serve обслуживает сокет аутентифицированного пользователя: snapshot → догонка → живые события.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.origins})
	if err != nil {
		return
	}
	c := &client{userID: userID, conn: conn, wake: make(chan struct{}, 1), progress: make(chan []byte, 64)}
	h.add(c) // до snapshot: ни одно уведомление после него не потеряется
	defer h.remove(c)

	ctx := conn.CloseRead(r.Context())
	err = h.serve(ctx, c)
	if errors.Is(err, context.Canceled) || websocket.CloseStatus(err) != -1 {
		conn.Close(websocket.StatusNormalClosure, "")
		return
	}
	conn.Close(websocket.StatusInternalError, "")
}

func (h *Hub) serve(ctx context.Context, c *client) error {
	last, err := h.sendSnapshot(ctx, c)
	if err != nil {
		return err
	}
	ping := time.NewTicker(h.ping)
	defer ping.Stop()
	c.poke()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.wake:
			if last, err = h.catchUp(ctx, c, last); err != nil {
				return err
			}
		case msg := <-c.progress:
			if err := write(ctx, c.conn, msg); err != nil {
				return err
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 3*h.ping)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}

// sendSnapshot — system.snapshot с seq момента выдачи (FR-IF-5): баланс и активные задачи.
func (h *Hub) sendSnapshot(ctx context.Context, c *client) (int64, error) {
	var (
		seq     int64
		payload []byte
	)
	err := pgx.BeginTxFunc(ctx, h.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT last_seq FROM user_seq WHERE user_id = $1), 0)`, c.userID).Scan(&seq); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT json_build_object(
				'wallets', COALESCE((SELECT json_agg(json_build_object(
					'wallet_id', id, 'currency_id', currency_id,
					'balance', to_char(balance, 'FM999999999990.0000'), 'reserved', to_char(reserved, 'FM999999999990.0000')))
					FROM wallets WHERE owner_kind = 'user' AND owner_id = $1), '[]'::json),
				'active_tasks', COALESCE((SELECT json_agg(json_build_object(
					'task_id', id, 'form_id', form_id, 'status', status, 'attempts', attempts) ORDER BY created_at)
					FROM system_tasks WHERE user_id = $1 AND status IN ('pending', 'processing')), '[]'::json))`,
			c.userID).Scan(&payload)
	})
	if err != nil {
		return 0, err
	}
	ev, _ := json.Marshal(Event{EventID: uuid.NewString(), Type: "system.snapshot", Seq: seq,
		OccurredAt: time.Now().UTC(), Payload: payload})
	return seq, write(ctx, c.conn, ev)
}

// catchUp отправляет доставленные события с seq > last по порядку.
func (h *Hub) catchUp(ctx context.Context, c *client, last int64) (int64, error) {
	for {
		rows, err := h.pool.Query(ctx, `SELECT id, type, seq, created_at, payload FROM outbox_queue
			WHERE user_id = $1 AND status = 'sent' AND seq > $2 ORDER BY seq LIMIT $3`, c.userID, last, catchUpBatch)
		if err != nil {
			return last, err
		}
		events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
			var (
				id int64
				ev Event
			)
			err := r.Scan(&id, &ev.Type, &ev.Seq, &ev.OccurredAt, &ev.Payload)
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], uint64(id))
			ev.EventID = uuid.NewSHA1(eventNS, b[:]).String()
			ev.OccurredAt = ev.OccurredAt.UTC()
			return ev, err
		})
		if err != nil {
			return last, err
		}
		for _, ev := range events {
			raw, _ := json.Marshal(ev)
			if err := write(ctx, c.conn, raw); err != nil {
				return last, err
			}
			last = ev.Seq
		}
		if len(events) < catchUpBatch {
			return last, nil
		}
	}
}

func write(ctx context.Context, conn *websocket.Conn, msg []byte) error {
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, msg)
}
