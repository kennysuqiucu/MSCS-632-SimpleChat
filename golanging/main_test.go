package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
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
		{"part of a word does not match", txt(searchKeyword(db, "lunc")), []string{}},
		{"punctuation is not part of a word", txt(searchKeyword(db, "today")), []string{"Lunch today?"}},
		{"% and _ split words", txt(searchKeyword(db, "branch")), []string{"100% done, see my_branch"}},
		{"% is not a wildcard", txt(searchKeyword(db, "%")), []string{}},
		{"no match", txt(searchKeyword(db, "pizza")), []string{}},
	}
	for _, c := range cases {
		if !equal(c.got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestSearchWholeWords mirrors the demo: "search is" must not match "this" or "finished".
func TestSearchWholeWords(t *testing.T) {
	db := testDB(t)
	mustSave(t, db, "carol", "reminder: demo freeze is tonight")
	mustSave(t, db, "alice", "I finished the database functions this morning")
	mustSave(t, db, "alice", "what is left on the list?")
	mustSave(t, db, "bob", "yep, I'll be there")
	cases := map[string][]string{
		"is":   {"reminder: demo freeze is tonight", "what is left on the list?"},
		"IS":   {"reminder: demo freeze is tonight", "what is left on the list?"},
		"list": {"what is left on the list?"},
		"i'll": {"yep, I'll be there"},
		"ill":  {},
	}
	for kw, want := range cases {
		got, err := searchKeyword(db, kw)
		if !equal(texts(t, got, err), want) {
			t.Errorf("search %q: got %q, want %q", kw, texts(t, got, err), want)
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

func TestLoadConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conv.txt")
	content := "# comment\n\nalice: hi: there\n  bob:   hello  \ncarol: one\nbob:\nalice: two\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, count, err := loadConversation(path)
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Errorf("count = %d, want 5", count)
	}
	want := map[string][]string{
		"alice": {"hi: there", "two"}, // only the first ":" splits user from text
		"bob":   {"hello", ""},        // "bob:" is kept as an empty message
		"carol": {"one"},
	}
	for u, w := range want {
		if !equal(got[u], w) {
			t.Errorf("%s: got %q, want %q", u, got[u], w)
		}
	}

	for name, bad := range map[string]string{
		"no colon":     "alice hello\n",
		"unknown user": "dave: hi\n",
	} {
		os.WriteFile(path, []byte(bad), 0o644)
		if _, _, err := loadConversation(path); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, _, err := loadConversation(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Error("missing file: expected an error")
	}
}

// TestSampleConversation checks the shared messages.txt in the repo root:
// every line except bob's empty one is saved.
func TestSampleConversation(t *testing.T) {
	conv, count, err := loadConversation("../messages.txt")
	if err != nil {
		t.Fatal(err)
	}
	db := testDB(t)
	for _, u := range users {
		for _, text := range conv[u] {
			saveMessage(db, Message{UserID: u, Text: text, SentAt: time.Now()}) // empty one fails on purpose
		}
	}
	all, err := history(db)
	if err != nil || len(all) != count-1 {
		t.Fatalf("saved %d of %d messages, want %d (err=%v)", len(all), count, count-1, err)
	}
}

// scripts is a small conversation used by the concurrency test.
var scripts = map[string][]string{
	"alice": {"Good morning everyone!", "Anyone up for lunch today?", "The build is green again"},
	"bob":   {"Morning alice", "Lunch sounds good to me", "I pushed a fix for the login bug"},
	"carol": {"Hi all", "Count me in for lunch", "Who is reviewing my pull request?"},
}

// TestConcurrentUsers runs the real pipeline: user goroutines -> channel ->
// one saver goroutine -> SQLite.
func TestConcurrentUsers(t *testing.T) {
	db := testDB(t)
	msgs := make(chan Message)
	var pending sync.WaitGroup
	done := make(chan struct{})
	go saveLoop(db, msgs, &pending, done)

	var senders sync.WaitGroup
	for _, u := range users {
		senders.Add(1)
		go func() {
			defer senders.Done()
			simulateUser(u, scripts[u], msgs, &pending)
		}()
	}
	senders.Wait()
	pending.Wait()

	for _, u := range users {
		got, err := filterByUser(db, u)
		if err != nil || len(got) != len(scripts[u]) {
			t.Fatalf("%s: got %d msgs, want %d (err=%v)", u, len(got), len(scripts[u]), err)
		}
		// Each user's own messages stay in the order that user sent them.
		if !equal(texts(t, got, nil), scripts[u]) {
			t.Errorf("%s: order changed: %q", u, texts(t, got, nil))
		}
	}
	close(msgs)
	<-done
}
