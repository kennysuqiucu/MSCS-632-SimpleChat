package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "test.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustSave(t *testing.T, db *sql.DB, user, text string) {
	t.Helper()
	if _, err := saveMessage(db, Message{UserID: user, Text: text, SentAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func texts(t *testing.T, msgs []Message, err error) []string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, m := range msgs {
		out = append(out, m.Text)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSaveAndQuery(t *testing.T) {
	db := testDB(t)
	sentAt := time.Date(2026, 10, 3, 14, 5, 9, 123456789, time.Local)
	id, err := saveMessage(db, Message{UserID: "alice", Text: "Lunch today?", SentAt: sentAt})
	if err != nil || id != 1 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	mustSave(t, db, "bob", "lunch sounds good")
	mustSave(t, db, "carol", "100% done, see my_branch")

	all, err := history(db)
	if err != nil || len(all) != 3 {
		t.Fatalf("history: %d msgs, err=%v", len(all), err)
	}
	if got := all[0]; got.UserID != "alice" || !got.SentAt.Equal(sentAt) {
		t.Fatalf("round trip changed the message: %+v", got)
	}

	// Go only forwards (msgs, err) directly into a function taking exactly those.
	txt := func(msgs []Message, err error) []string { return texts(t, msgs, err) }
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"filter bob", txt(filterByUser(db, "bob")), []string{"lunch sounds good"}},
		{"filter unknown", txt(filterByUser(db, "dave")), []string{}},
		{"keyword ignores case", txt(searchKeyword(db, "LUNCH")), []string{"Lunch today?", "lunch sounds good"}},
		{"% is literal", txt(searchKeyword(db, "100%")), []string{"100% done, see my_branch"}},
		{"_ is literal", txt(searchKeyword(db, "y_b")), []string{"100% done, see my_branch"}},
		{"no match", txt(searchKeyword(db, "pizza")), []string{}},
	}
	for _, c := range cases {
		if !equal(c.got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestDatabaseRejectsEmptyMessage(t *testing.T) {
	db := testDB(t)
	if _, err := saveMessage(db, Message{UserID: "bob", Text: "", SentAt: time.Now()}); err == nil {
		t.Fatal("empty message was saved; the CHECK constraint should reject it")
	}
	if all, err := history(db); err != nil || len(all) != 0 {
		t.Fatalf("history after rejected insert: %d msgs, err=%v", len(all), err)
	}
}

func TestFreshAndKeep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	for _, step := range []struct {
		fresh bool
		want  int
	}{{true, 1}, {false, 2}, {true, 1}} {
		db, err := openDB(path, step.fresh)
		if err != nil {
			t.Fatal(err)
		}
		mustSave(t, db, "alice", "hi")
		all, err := history(db)
		db.Close()
		if err != nil || len(all) != step.want {
			t.Fatalf("fresh=%v: got %d msgs, want %d (err=%v)", step.fresh, len(all), step.want, err)
		}
		if step.fresh && all[0].ID != 1 {
			t.Fatalf("fresh database should number messages from 1, got %d", all[0].ID)
		}
	}
}

