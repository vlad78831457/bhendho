package outbox

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"offgrid/core/internal/core/db/dbtest"
)

func TestSeqPerUserAndNotify(t *testing.T) {
	pool, url := dbtest.New(t)
	ctx := context.Background()
	d := New(pool, 3, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	listener, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close(ctx)
	if _, err := listener.Exec(ctx, "LISTEN "+Channel); err != nil {
		t.Fatal(err)
	}

	a, b := uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{a, b, a, a, b} {
		if _, err := pool.Exec(ctx, `INSERT INTO outbox_queue (user_id, type, payload) VALUES ($1, 'task.status', '{}')`, u); err != nil {
			t.Fatal(err)
		}
	}
	total := 0
	for {
		n, err := d.DispatchOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		total += n
	}
	if total != 5 {
		t.Fatalf("dispatched %d, want 5", total)
	}

	for user, want := range map[uuid.UUID][]int64{a: {1, 2, 3}, b: {1, 2}} {
		rows, err := pool.Query(ctx, `SELECT seq FROM outbox_queue WHERE user_id = $1 ORDER BY id`, user)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("user seqs %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("user seqs %v, want %v (monotonic by id)", got, want)
			}
		}
	}

	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := listener.WaitForNotification(wctx); err != nil {
		t.Fatalf("no NOTIFY after dispatch: %v", err)
	}
}
