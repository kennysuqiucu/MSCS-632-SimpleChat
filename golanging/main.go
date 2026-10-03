// Command gochat is a small text-based chat room. Three simulated users
// (alice, bob and carol) send messages at the same time; every message is
// saved to a SQLite database, which can then be filtered by user or searched
// by keyword.
//
// This first version contains the Message struct and the database functions.
// The temporary main below exercises them; it is replaced when the goroutines
// and command loop are added.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers itself as "sqlite"
)

// ---------------------------------------------------------------------------
// Message

// Message is one chat message. It maps directly onto a row of the messages
// table: id, user_id, message, sent_at.
type Message struct {
	ID     int64 // assigned by the database when the message is saved
	UserID string
	Text   string
	SentAt time.Time
}

func (m Message) String() string {
	return fmt.Sprintf("[%s] #%d %s: %s", m.SentAt.Format("15:04:05.000"), m.ID, m.UserID, m.Text)
}

// ---------------------------------------------------------------------------
// Database

const schema = `
CREATE TABLE IF NOT EXISTS messages (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id TEXT NOT NULL,
	message TEXT NOT NULL CHECK (message <> ''),  -- the database rejects empty messages
	sent_at TEXT NOT NULL  -- RFC 3339 timestamp, e.g. 2026-10-03T14:05:09.123Z
)`

// openDB opens (or creates) the database file and makes sure the table exists.
// If fresh is true, the table is dropped and recreated, which removes messages
// from earlier runs and restarts IDs at 1.
func openDB(path string, fresh bool) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// SQLite allows only one writer at a time. A single connection means our
	// own queries can never collide with "database is locked".
	db.SetMaxOpenConns(1)

	if fresh {
		if _, err := db.Exec(`DROP TABLE IF EXISTS messages`); err != nil {
			db.Close()
			return nil, fmt.Errorf("clear messages: %w", err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create table: %w", err)
	}
	return db, nil
}

// saveMessage inserts m and returns the ID the database assigned to it.
func saveMessage(db *sql.DB, m Message) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO messages (user_id, message, sent_at) VALUES (?, ?, ?)`,
		m.UserID, m.Text, m.SentAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("save message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read new message id: %w", err)
	}
	return id, nil
}

// history returns every saved message, oldest first.
func history(db *sql.DB) ([]Message, error) {
	return queryMessages(db,
		`SELECT id, user_id, message, sent_at FROM messages ORDER BY id`)
}

// filterByUser returns only the messages sent by userID.
func filterByUser(db *sql.DB, userID string) ([]Message, error) {
	return queryMessages(db,
		`SELECT id, user_id, message, sent_at FROM messages WHERE user_id = ? ORDER BY id`,
		userID)
}

// searchKeyword returns messages whose text contains keyword (ignoring case).
func searchKeyword(db *sql.DB, keyword string) ([]Message, error) {
	// Escape LIKE wildcards so a search for "50%" means the text "50%".
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword)
	return queryMessages(db,
		`SELECT id, user_id, message, sent_at FROM messages
		 WHERE message LIKE '%' || ? || '%' ESCAPE '\' ORDER BY id`,
		escaped)
}

// queryMessages runs a SELECT and turns each row into a Message.
func queryMessages(db *sql.DB, query string, args ...any) ([]Message, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		var sentAt string
		if err := rows.Scan(&m.ID, &m.UserID, &m.Text, &sentAt); err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		if m.SentAt, err = time.Parse(time.RFC3339Nano, sentAt); err != nil {
			return nil, fmt.Errorf("message %d has a bad sent_at %q: %w", m.ID, sentAt, err)
		}
		m.SentAt = m.SentAt.Local()
		msgs = append(msgs, m)
	}
	// rows.Next returns false on errors too, so this check is required.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read rows: %w", err)
	}
	return msgs, nil
}

// ---------------------------------------------------------------------------
// Temporary main: checks the database functions until message handling exists.

func main() {
	db, err := openDB("chat.db", true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer db.Close()

	for _, m := range []Message{
		{UserID: "alice", Text: "Anyone up for lunch today?"},
		{UserID: "bob", Text: "Lunch sounds good to me"},
		{UserID: "carol", Text: "Who is reviewing my pull request?"},
	} {
		m.SentAt = time.Now()
		if _, err := saveMessage(db, m); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}

	// show takes (msgs, err) directly, so it can wrap any query call.
	show := func(msgs []Message, err error) {
		if err != nil {
			fmt.Fprintln(os.Stderr, "  error:", err)
			return
		}
		for _, m := range msgs {
			fmt.Println(" ", m)
		}
	}
	fmt.Println("history:")
	show(history(db))
	fmt.Println("filter by user bob:")
	show(filterByUser(db, "bob"))
	fmt.Println(`search "lunch":`)
	show(searchKeyword(db, "lunch"))
}
