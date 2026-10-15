package engine

import (
	"encoding/json"
	"testing"
)

// ADR-68: форма задачи просит пометки пользователя — прораб получает их в снимке, и только их.
func TestUserMarksInWorkerSnapshot(t *testing.T) {
	s := setup(t)
	u := s.user("1000")
	_, err := s.e.pool.Exec(s.ctx, `UPDATE users SET role = 'tester' WHERE id = $1`, u)
	must(t, err)
	created, err := s.create(u, s.hero(u, true), "k1")
	must(t, err)
	if _, leaked := created["user_marks"]; leaked {
		t.Fatal("user marks are for the prorab, not in the user's envelope")
	}
	c := s.claimOne()
	if c == nil {
		t.Fatal("nothing to claim")
	}
	raw, _ := json.Marshal(c.Blank)
	must(t, s.v.ValidateBlank(raw))
	marks, _ := c.Blank["user_marks"].(map[string]any)
	if len(marks) != 1 || marks["role"] != "tester" {
		t.Fatalf("user marks: %v", c.Blank["user_marks"])
	}
}
