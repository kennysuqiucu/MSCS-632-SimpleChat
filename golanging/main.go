// Command gochat is a small text-based chat room. Three simulated users
// (alice, bob and carol) send the messages listed in a text file at the same
// time; every message is saved to a SQLite database, which can then be
// filtered by user or searched by keyword from a simple command loop.
//
//	go run .                     # read ../messages.txt, fresh database (chat.db)
//	go run . -file other.txt     # read a different conversation file
//	go run . -keep               # keep messages from earlier runs
//	go run . -db other.db        # use a different database file
package main

import (
	"bufio"
	"database/sql"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"sync"
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
// Message handling

var users = []string{"alice", "bob", "carol"}

// simulateUser runs in its own goroutine and sends each of the user's lines
// into the channel, pausing briefly between messages. Because all users run
// at once, their messages interleave differently on every run.
func simulateUser(userID string, lines []string, out chan<- Message, pending *sync.WaitGroup) {
	for _, line := range lines {
		time.Sleep(time.Duration(rand.IntN(50)) * time.Millisecond)
		send(out, pending, userID, line)
	}
}

// send stamps a message with the current time and puts it on the channel.
// pending is marked done by the saver once the message is in the database.
func send(out chan<- Message, pending *sync.WaitGroup, userID, text string) {
	pending.Add(1)
	out <- Message{UserID: userID, Text: text, SentAt: time.Now()}
}

// saveLoop is the only goroutine that writes to the database. It receives
// messages until the channel is closed, then closes done.
func saveLoop(db *sql.DB, in <-chan Message, pending *sync.WaitGroup, done chan<- struct{}) {
	defer close(done)
	for m := range in {
		id, err := saveMessage(db, m)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  error: message from %s not saved: %v\n", m.UserID, err)
		} else {
			m.ID = id
			fmt.Println("  saved", m)
		}
		pending.Done()
	}
}

// ---------------------------------------------------------------------------
// Main program

const help = `commands:
  history              show every message
  user <name>          show only messages from alice, bob or carol
  search <word>        show only messages containing a word
  send <name> <text>   send a new message as alice, bob or carol
  help                 show this list
  quit                 exit`

// loadConversation reads a conversation file with one "user: message" per
// line and returns each user's lines in file order. Empty lines and lines
// starting with # are skipped. "bob:" with no text is kept as an empty message.
func loadConversation(path string) (map[string][]string, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open conversation file: %w", err)
	}
	defer f.Close()

	lines := map[string][]string{}
	count, lineNo := 0, 0
	in := bufio.NewScanner(f)
	for in.Scan() {
		lineNo++
		line := strings.TrimSpace(in.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		user, text, ok := strings.Cut(line, ":") // only the first ":" separates
		user = strings.TrimSpace(user)
		if !ok {
			return nil, 0, fmt.Errorf("%s line %d: expected \"user: message\", got %q", path, lineNo, line)
		}
		if !slices.Contains(users, user) {
			return nil, 0, fmt.Errorf("%s line %d: unknown user %q (use alice, bob or carol)", path, lineNo, user)
		}
		lines[user] = append(lines[user], strings.TrimSpace(text))
		count++
	}
	if err := in.Err(); err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", path, err)
	}
	return lines, count, nil
}

func main() {
	dbPath := flag.String("db", "chat.db", "SQLite database file")
	file := flag.String("file", "../messages.txt", "conversation file, one \"user: message\" per line")
	keep := flag.Bool("keep", false, "keep messages from earlier runs")
	flag.Parse()

	conversation, count, err := loadConversation(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	db, err := openDB(*dbPath, !*keep)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer db.Close()

	msgs := make(chan Message, 16)
	var pending sync.WaitGroup // messages sent but not yet saved
	saverDone := make(chan struct{})
	go saveLoop(db, msgs, &pending, saverDone)

	fmt.Printf("read %d messages from %s; alice, bob and carol are chatting...\n", count, *file)
	var senders sync.WaitGroup
	for _, u := range users {
		senders.Add(1)
		go func() {
			defer senders.Done()
			simulateUser(u, conversation[u], msgs, &pending)
		}()
	}
	senders.Wait()
	pending.Wait() // every message is now in the database

	fmt.Println()
	fmt.Println(help)
	runCommands(db, msgs, &pending)

	close(msgs) // tells saveLoop there is nothing more to save
	<-saverDone
	fmt.Println("bye")
}

// runCommands reads commands from standard input until "quit" or end of input.
func runCommands(db *sql.DB, msgs chan<- Message, pending *sync.WaitGroup) {
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !in.Scan() {
			return
		}
		cmd, arg, _ := strings.Cut(strings.TrimSpace(in.Text()), " ")
		arg = strings.TrimSpace(arg)

		var results []Message
		var err error
		switch strings.ToLower(cmd) {
		case "":
			continue
		case "quit", "exit":
			return
		case "help":
			fmt.Println(help)
			continue
		case "history":
			results, err = history(db)
		case "user":
			if !slices.Contains(users, arg) {
				fmt.Println("usage: user <alice|bob|carol>")
				continue
			}
			results, err = filterByUser(db, arg)
		case "search":
			if arg == "" {
				fmt.Println("usage: search <word>")
				continue
			}
			results, err = searchKeyword(db, arg)
		case "send":
			name, text, _ := strings.Cut(arg, " ")
			text = strings.TrimSpace(text)
			if !slices.Contains(users, name) || text == "" {
				fmt.Println("usage: send <alice|bob|carol> <text>")
				continue
			}
			send(msgs, pending, name, text)
			pending.Wait() // wait for the saver so the next query includes it
			continue
		default:
			fmt.Println("unknown command; type help")
			continue
		}

		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		if len(results) == 0 {
			fmt.Println("  (no messages)")
		}
		for _, m := range results {
			fmt.Println(" ", m)
		}
	}
}
