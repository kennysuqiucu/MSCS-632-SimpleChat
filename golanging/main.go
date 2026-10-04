// Command gochat is a small text-based chat room. Three simulated users
// (alice, bob and carol) send the messages listed in a text file at the same
// time; every message is saved to a SQLite database, which can then be
// filtered by user or searched by keyword from a simple command loop.
//
//	go run .                     # read ../messages.txt, fresh database (chat.db)
//	go run . -file other.txt     # read a different conversation file
//	go run . -keep               # keep messages from earlier runs
//	go run . -db other.db        # use a different database file
//
// How to read this file (top to bottom):
//
//  1. Message           the struct that holds one chat message
//  2. Database          functions that create the table, save, and query (Pradeep)
//  3. Message handling  goroutines for the users and the one goroutine that saves (Amar)
//  4. Main program      reads messages.txt, starts everything, runs the command loop (Amar)
//
// What happens when the program runs:
//
//	main ─► loadConversation (read messages.txt)
//	     ─► openDB (create chat.db and the messages table)
//	     ─► start saveLoop goroutine (the only code that writes to the database)
//	     ─► start one simulateUser goroutine per user ─► they send into the msgs channel
//	     ─► wait until every message is saved
//	     ─► runCommands (history / filter / search / send / quit)
//	     ─► close the channel, wait for saveLoop to finish, exit
package main

// Every package this file uses. Go refuses to compile if an import is unused.
import (
	"bufio"        // reads a file or the keyboard one line at a time
	"database/sql" // Go's standard database interface
	"flag"         // command-line options such as -keep
	"fmt"          // printing and building strings
	"math/rand/v2" // random numbers for the users' short pauses
	"os"           // files, standard input/output, exiting
	"slices"       // helpers for slices (Go's growable lists), e.g. slices.Contains
	"strings"      // string helpers: split, trim, lower case
	"sync"         // sync.WaitGroup, a counter for waiting on goroutines
	"time"         // timestamps and sleeping
	"unicode"      // checks whether a character is a letter or a digit

	// The underscore means "import only for its side effect": the driver
	// registers itself with database/sql under the name "sqlite". We never call
	// it directly. It is written in pure Go, so no C compiler is needed.
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Message

// Message is one chat message. It holds the same data as a row of the
// messages table: id, user_id, message, sent_at.
//
// Go note: a struct is just a set of named fields (no classes, no
// constructors). Fields starting with a capital letter are public.
type Message struct {
	ID      int64     // assigned by the database when the message is saved
	UserID  string    // who sent it: "alice", "bob" or "carol"
	Message string    // the message text
	SentAt  time.Time // when the user sent it
}

// String controls how a Message looks when printed, for example
// "[14:05:09.123] #12 bob: hello".
//
// Go note: "(m Message)" makes this a method on Message. Any type with a
// String() method is printed this way by fmt.Println automatically.
func (m Message) String() string {
	return fmt.Sprintf("[%s] #%d %s: %s", m.SentAt.Format("15:04:05.000"), m.ID, m.UserID, m.Message)
}

// ---------------------------------------------------------------------------
// Database
//
// Every function here takes the database handle (*sql.DB) and returns an
// error as its last value. Go has no exceptions: the caller must check
// "if err != nil" itself. Nothing forces the check, so we do it every time.

// schema is the SQL that creates the messages table.
// The backticks make a raw string, so the SQL can span several lines.
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
//
// Go note: a function can return several values. Here it returns the
// database handle AND an error; exactly one of them is meaningful.
func openDB(path string, fresh bool) (*sql.DB, error) {
	// ":=" declares a new variable and assigns it in one step.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		// %w wraps the original error, so the message keeps the root cause.
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// SQLite allows only one writer at a time. A single connection means our
	// own queries can never collide with "database is locked".
	db.SetMaxOpenConns(1)

	if fresh {
		// "if x := ...; cond" runs the statement, then tests the condition.
		// The "_" throws away the first return value, which we don't need.
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
// An empty message breaks the CHECK rule in the schema, so the database
// refuses it and this function returns that error.
func saveMessage(db *sql.DB, m Message) (int64, error) {
	// The "?" placeholders are filled in safely by the driver, in order.
	// Never build SQL by gluing strings together (that allows SQL injection).
	res, err := db.Exec(
		`INSERT INTO messages (user_id, message, sent_at) VALUES (?, ?, ?)`,
		m.UserID, m.Message, m.SentAt.UTC().Format(time.RFC3339Nano))
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

// searchKeyword returns messages that contain keyword as a whole word (ignoring case).
//
// It matches whole words only, the same as the Rust version: searching for
// "is" finds "freeze is tonight" but not "finished" or "this". The SQL LIKE
// first picks every message containing the letters anywhere, then hasWord
// keeps only the messages where those letters are a word on their own.
func searchKeyword(db *sql.DB, keyword string) ([]Message, error) {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	// Escape LIKE wildcards so a search for "50%" means the text "50%".
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword)
	// Step 1: let SQLite find every message that contains the letters.
	candidates, err := queryMessages(db,
		`SELECT id, user_id, message, sent_at FROM messages
		 WHERE message LIKE '%' || ? || '%' ESCAPE '\' ORDER BY id`,
		escaped)
	if err != nil {
		return nil, err
	}

	// Step 2: keep only the messages where the keyword is a whole word.
	// Go note: "var matches []Message" starts as an empty (nil) slice, and
	// append grows it as needed. "for _, m := range" loops over each item;
	// the "_" ignores the index.
	var matches []Message
	for _, m := range candidates {
		if hasWord(m.Message, keyword) {
			matches = append(matches, m)
		}
	}
	return matches, nil
}

// hasWord reports whether one of the words in text is exactly keyword
// (keyword must already be lower case). The text is split wherever a
// character is not a letter, a digit or an apostrophe, so "tonight?" is read
// as the word "tonight".
//
// Go note: functions are values. The small func passed to FieldsFunc decides,
// for each character (a "rune"), whether it separates words.
func hasWord(text, keyword string) bool {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
	return slices.Contains(words, keyword)
}

// queryMessages runs a SELECT and turns each row into a Message.
// history, filterByUser and searchKeyword all use it.
//
// Go note: "args ...any" means "any number of extra values of any type",
// and "args..." passes them all on to db.Query.
func queryMessages(db *sql.DB, query string, args ...any) ([]Message, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	// defer runs rows.Close() when this function returns, on every path,
	// so the query is always cleaned up even if we return early with an error.
	defer rows.Close()

	var msgs []Message
	for rows.Next() { // move to the next row; false when there are no more
		var m Message
		var sentAt string
		// Scan copies the row's columns into these variables BY POSITION
		// (id, user_id, message, sent_at), not by name. There is no ORM.
		// "&m.ID" passes a pointer, so Scan can write into the field.
		if err := rows.Scan(&m.ID, &m.UserID, &m.Message, &sentAt); err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		// The time is stored as text in UTC; turn it back into a time.Time.
		if m.SentAt, err = time.Parse(time.RFC3339Nano, sentAt); err != nil {
			return nil, fmt.Errorf("message %d has a bad sent_at %q: %w", m.ID, sentAt, err)
		}
		m.SentAt = m.SentAt.Local() // show it in the computer's local time
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
//
// Go note on goroutines and channels:
//   - "go f()" starts f running at the same time as the rest of the program.
//     That is a goroutine: much lighter than an operating-system thread.
//   - A channel (chan Message) is a pipe that goroutines use to pass values.
//     "ch <- v" sends v into it; "v := <-ch" or "for v := range ch" receives.
//   - "chan<- Message" means this function may only SEND on the channel,
//     "<-chan Message" means it may only RECEIVE. The compiler enforces it.
//
// The design: every user goroutine sends into ONE channel, and ONE goroutine
// (saveLoop) receives from it and writes to SQLite. Since only saveLoop ever
// writes, SQLite never sees two writers at once.

// users is the fixed list of simulated users. A line in messages.txt for any
// other name is an error.
var users = []string{"alice", "bob", "carol"}

// simulateUser runs in its own goroutine and sends each of the user's lines
// into the channel, pausing briefly between messages. Because all users run
// at once, their messages interleave differently on every run.
func simulateUser(userID string, lines []string, out chan<- Message, pending *sync.WaitGroup) {
	for _, line := range lines {
		// Wait a random 0-49 ms, like a person pausing between messages.
		time.Sleep(time.Duration(rand.IntN(50)) * time.Millisecond)
		send(out, pending, userID, line)
	}
}

// send stamps a message with the current time and puts it on the channel.
// pending is marked done by the saver once the message is in the database.
//
// pending is a *sync.WaitGroup (a pointer), so every goroutine shares the
// same counter instead of getting its own copy.
func send(out chan<- Message, pending *sync.WaitGroup, userID, text string) {
	pending.Add(1) // one more message waiting to be saved
	out <- Message{UserID: userID, Message: text, SentAt: time.Now()}
}

// saveLoop is the only goroutine that writes to the database. It receives
// messages until the channel is closed, then closes done.
func saveLoop(db *sql.DB, in <-chan Message, pending *sync.WaitGroup, done chan<- struct{}) {
	// Closing done tells main "I have finished". struct{} is an empty value:
	// the channel carries no data, only the signal.
	defer close(done)
	// This loop waits for each message and ends when main calls close(msgs).
	for m := range in {
		id, err := saveMessage(db, m)
		if err != nil {
			// Errors are values: we print this one and keep going, so one bad
			// message (bob's empty line) does not stop the program.
			fmt.Fprintf(os.Stderr, "  error: message from %s not saved: %v\n", m.UserID, err)
		} else {
			m.ID = id
			fmt.Println("  saved", m)
		}
		pending.Done() // this message is finished, saved or not
	}
}

// ---------------------------------------------------------------------------
// Main program

// help is the command list printed at startup and by the "help" command.
const help = `commands:
  history              show every message
  filter <name>        show only messages from alice, bob or carol
  search <word>        show only messages containing a word
  send <name> <text>   send a new message as alice, bob or carol
  help                 show this list
  quit                 exit`

// loadConversation reads a conversation file with one "user: message" per
// line and returns each user's lines in file order. Empty lines and lines
// starting with # are skipped. "bob:" with no text is kept as an empty message.
//
// It returns three values: a map from user name to that user's lines, how
// many messages were read, and an error.
func loadConversation(path string) (map[string][]string, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open conversation file: %w", err)
	}
	defer f.Close() // close the file when the function returns

	// map[string][]string: each user name points to a list of their messages.
	lines := map[string][]string{}
	count, lineNo := 0, 0
	in := bufio.NewScanner(f)
	for in.Scan() { // reads one line per loop; false at the end of the file
		lineNo++
		line := strings.TrimSpace(in.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue // skip blank lines and comments
		}
		// Cut splits at the FIRST ":" only, so "carol: reminder: demo freeze"
		// gives user "carol" and text "reminder: demo freeze".
		user, text, ok := strings.Cut(line, ":")
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
	// Like rows.Err(): Scan returns false on a read error too.
	if err := in.Err(); err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", path, err)
	}
	return lines, count, nil
}

// main is where the program starts.
func main() {
	// Command-line options. Each flag function returns a pointer; "*file"
	// reads the value it points to after flag.Parse() fills it in.
	dbPath := flag.String("db", "chat.db", "SQLite database file")
	file := flag.String("file", "../messages.txt", "conversation file, one \"user: message\" per line")
	keep := flag.Bool("keep", false, "keep messages from earlier runs")
	flag.Parse()

	// Step 1: read the conversation. Without it there is nothing to send.
	conversation, count, err := loadConversation(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Step 2: open the database. A fresh table unless -keep was given.
	db, err := openDB(*dbPath, !*keep)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer db.Close()

	// Step 3: create the channel and start the one goroutine that saves.
	// The 16 is the buffer: up to 16 messages can wait in the channel before
	// a sender has to pause.
	msgs := make(chan Message, 16)
	var pending sync.WaitGroup // messages sent but not yet saved
	saverDone := make(chan struct{})
	go saveLoop(db, msgs, &pending, saverDone)

	// Step 4: start one goroutine per user. They all run at the same time.
	fmt.Printf("read %d messages from %s; alice, bob and carol are chatting...\n", count, *file)
	var senders sync.WaitGroup // users still sending
	for _, u := range users {
		senders.Add(1)
		go func() { // an anonymous function, started as a goroutine
			defer senders.Done() // when this user is finished, count down
			simulateUser(u, conversation[u], msgs, &pending)
		}()
	}
	// Step 5: wait for every user to finish sending, then for every message
	// to be saved, so the first command sees the whole history.
	senders.Wait()
	pending.Wait() // every message is now in the database

	// Step 6: the interactive command loop, until "quit".
	fmt.Println()
	fmt.Println(help)
	runCommands(db, msgs, &pending)

	// Step 7: shut down cleanly. In Go we close the channel ourselves; that
	// ends saveLoop's "for range" loop. Then wait for its signal.
	close(msgs) // tells saveLoop there is nothing more to save
	<-saverDone // blocks until saveLoop closes saverDone
	fmt.Println("bye")
}

// runCommands reads commands from standard input until "quit" or end of input.
// Queries read the database directly (database/sql is safe to share between
// goroutines); "send" goes through the channel like the simulated users.
func runCommands(db *sql.DB, msgs chan<- Message, pending *sync.WaitGroup) {
	in := bufio.NewScanner(os.Stdin)
	for { // a "for" with no condition loops until a return
		fmt.Print("> ")
		if !in.Scan() {
			return // end of input (for example Ctrl+Z on Windows)
		}
		// Split the line into the command word and the rest.
		cmd, arg, _ := strings.Cut(strings.TrimSpace(in.Text()), " ")
		arg = strings.TrimSpace(arg)

		var results []Message
		var err error
		// Go's switch needs no "break": each case stops by itself.
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
		case "filter", "user": // "filter" matches the Rust version; "user" still works
			if !slices.Contains(users, arg) {
				fmt.Println("usage: filter <alice|bob|carol>")
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

		// history, filter and search all end up here to print their results.
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		if len(results) == 0 {
			fmt.Println("  (no messages)")
		}
		for _, m := range results {
			fmt.Println(" ", m) // uses Message.String() above
		}
	}
}
