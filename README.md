# gochat (Go version)

Three simulated users (alice, bob and carol) chat in one shared room at the same time, sending the messages listed in `messages.txt`. Every message is saved to SQLite. You can then show the history, filter it by user, or search it by keyword.

The Go code is in `golanging/`; the Rust version is in `rust/` (see `rust/README.md`). Run the Go commands from inside `golanging`:

```
cd golanging
go run .                   # read ../messages.txt, fresh chat.db, then the command loop
go run . -file other.txt   # read a different conversation file
go run . -keep             # keep messages from earlier runs
go run . -db x.db          # use a different database file
go test -v .               # tests
```

## The conversation file

`messages.txt` in the repo root has one message per line, written as `user: message`:

```
# lines starting with # and empty lines are ignored
alice: hey everyone, are we still on for rust tonight?
carol: reminder: demo freeze is tonight
bob:
```

- Only the first `:` separates the user from the text, so `carol: reminder: demo freeze is tonight` works.
- The user must be `alice`, `bob` or `carol`. Anything else stops the program with the line number.
- `bob:` with no text is sent as an empty message **on purpose**. The database rejects it, which shows a Go error being handled as a value: `error: message from bob not saved: ... CHECK constraint failed`.
- The sample file has 101 lines, so a run saves 100 messages and rejects 1.

You need **Go 1.26 or newer**. The SQLite driver is `modernc.org/sqlite`, which is written in pure Go, so no C compiler (gcc) is needed.

## Commands

```
history              show every message
user <name>          show only messages from alice, bob or carol
search <word>        show only messages containing a word (ignores case)
send <name> <text>   send a new message as alice, bob or carol
help                 show the command list
quit                 exit
```

## Shared contract with the Rust version

Both versions should match these so they can be compared side by side.

**Table**

```sql
CREATE TABLE IF NOT EXISTS messages (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    message TEXT NOT NULL CHECK (message <> ''),  -- the database rejects empty messages
    sent_at TEXT NOT NULL  -- RFC 3339 timestamp in UTC
)
```

**Behavior**

- Users are `alice`, `bob` and `carol`. Each one sends their own lines from `messages.txt`, in file order.
- Each user sleeps a random 0–50 ms before each message, so messages from different users interleave differently on every run. Each user's own messages always stay in order.
- `sent_at` is set when the user sends the message, and `id` is set by the database.
- A fresh run drops and recreates the table (`DROP TABLE IF EXISTS messages`), so old messages are gone and IDs start at 1.
- Results are ordered by `id`.
- Keyword search uses `LIKE` (case-insensitive for ASCII text). `%` and `_` in a search are treated as plain characters.
- The output line format is `[15:04:05.000] #id user: text`.

## Code layout (main.go)

| Section | Owner | Contents |
|---|---|---|
| Message | Pradeep | `Message` struct |
| Database | Pradeep | `openDB` (with the empty-message CHECK), `saveMessage`, `history`, `filterByUser`, `searchKeyword` |
| Message handling | Amar | `simulateUser` and `send` (user goroutines → channel), `saveLoop` (the single goroutine that writes to the DB) |
| Main program | Amar | `loadConversation` (reads `messages.txt`), `main` and `runCommands` (the command loop) |

## How it works

```
alice goroutine ─┐
bob goroutine   ─┼─► chan Message ─► saveLoop goroutine ─► SQLite (chat.db)
carol goroutine ─┘                                           ▲
command loop (send) ──► same channel                         │
command loop (history/user/search) ── reads ─────────────────┘
```

- **Only one goroutine writes**, so there are no "database is locked" errors. `db.SetMaxOpenConns(1)` also makes reads share that single connection.
- **Waiting for saves:** a `sync.WaitGroup` called `pending` counts messages that have been sent but not saved yet. `send` adds 1 and `saveLoop` marks it done after the insert. Waiting on it before a query means the query sees every message sent so far.
- **Shutdown:** closing the channel ends `saveLoop`'s `for m := range in` loop, and `main` waits for it to finish before exiting.

## Points for the language comparison

- **Errors are plain values.** Every DB call returns `(result, error)` and the code checks each one. Nothing forces the check, though: `saveMessage(db, m)` with the error ignored still compiles. Rust's `Result` makes you handle it.
- **`rows.Err()` is easy to forget.** `rows.Next()` returns false both at the end of the results and when an error happens. Only `rows.Err()` tells the two apart.
- **`database/sql` is a standard interface.** The driver is imported only for its side effect (`_ "modernc.org/sqlite"`), and switching databases means changing the driver name. `rusqlite` is SQLite-specific.
- **Concurrency safety.** Go's compiler doesn't stop two goroutines from sharing data unsafely. Here the design keeps it safe (one owner, plus a channel). Rust's ownership rules enforce this at compile time.
- **Unused imports and variables are compile errors** in Go. Rust gives warnings instead.

The previous in-memory version (direct messages, bots, benchmark) is kept in `_old/`. Go ignores folders that start with `_`, so it doesn't affect the build. Delete it if you don't need it.
