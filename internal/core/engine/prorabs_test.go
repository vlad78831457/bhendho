package engine

import (
	"testing"

	"github.com/google/uuid"

	"offgrid/core/internal/core/codes"
)

// ADR-76: задачу берёт прораб с наибольшим приоритетом на бланк; 0 — не берёт; отозванный не мешает.
func TestProrabPriorities(t *testing.T) {
	s := setup(t)
	admin := uuid.New()
	token, err := s.e.IssueServiceToken(s.ctx, "tale", "dumb")
	must(t, err)
	dumb, err := s.e.AuthenticateService(s.ctx, token)
	must(t, err)
	u := s.user("1000")
	hero := s.hero(u, true)
	claim := func(acc ServiceAccount) bool {
		c, err := s.e.Claim(s.ctx, acc, []int{1})
		must(t, err)
		if c != nil {
			_, err = s.e.Accept(s.ctx, acc, c.TaskID, false, 0) // отказаться: задача вернётся в очередь
			must(t, err)
		}
		return c != nil
	}

	_, err = s.create(u, hero, "k1")
	must(t, err)
	// Без настроек — оба равны (1): берёт любой.
	if !claim(s.acc) {
		t.Fatal("default priority must allow claiming")
	}
	must(t, s.e.SetProrabPriority(s.ctx, admin, "tale", "dumb", "tale.start.v1", 5))
	if claim(s.acc) {
		t.Fatal("a prorab with a lower priority must not take the form")
	}
	if !claim(dumb) {
		t.Fatal("the top prorab must take the form")
	}
	must(t, s.e.SetProrabPriority(s.ctx, admin, "tale", "dumb", "tale.start.v1", 0))
	if claim(dumb) {
		t.Fatal("priority 0 must switch the prorab off for the form")
	}
	if !claim(s.acc) {
		t.Fatal("after the top prorab is off, the next one takes the form")
	}
	// Отозванный прораб с высоким приоритетом очередь не держит.
	must(t, s.e.SetProrabPriority(s.ctx, admin, "tale", "dumb", "tale.start.v1", 9))
	_, err = s.e.pool.Exec(s.ctx, `UPDATE service_accounts SET revoked_at = now() WHERE name = 'dumb'`)
	must(t, err)
	if !claim(s.acc) {
		t.Fatal("a revoked prorab must not block the queue")
	}

	list, err := s.e.ProrabPriorities(s.ctx, "tale")
	must(t, err)
	if len(list) != 1 || list[0].Prorab != "prorab-1" || list[0].Priority != 1 || list[0].Explicit {
		t.Fatalf("list of active prorabs: %+v", list)
	}
	for name, c := range map[string][3]any{
		"negative":       {"prorab-1", "tale.start.v1", -1},
		"unknown prorab": {"nobody", "tale.start.v1", 1},
		"fact form":      {"prorab-1", "tale.character.v1", 1},
	} {
		if err := s.e.SetProrabPriority(s.ctx, admin, "tale", c[0].(string), c[1].(string), c[2].(int)); refusalCode(err) != codes.ValidationError {
			t.Errorf("%s: %v", name, err)
		}
	}
}
