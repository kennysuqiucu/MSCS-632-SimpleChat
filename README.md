# Simple Chat: Go vs Rust

MSCS-632 Advanced Programming Languages, group project (University of the Cumberlands).

The same small chat application is built twice, once in **Go** and once in **Rust**, so the two languages can be compared on the same problem. Three simulated users (alice, bob and carol) chat in one shared room at the same time. Every message is saved to SQLite, and you can then show the history, filter it by user, or search it by keyword.

## Team

| Member | Language | Work |
|---|---|---|
| Kenny Su Qiu | Rust | `Message` struct and database functions (`rust/src/db.rs`) |
| Sasha I Chen | Rust | Threads, channel and main program (`rust/src/main.rs`) |
| Pradeep Paladugula | Go | `Message` struct and database functions (`golanging/main.go`) |
| Amar Kulkarni | Go | Goroutines, channel and main program (`golanging/main.go`) |

## Repository layout

```
MSCS-632-SimpleChat/
├── README.md           this file
├── messages.txt        sample conversation, read by the Go version
├── .gitignore          keeps local *.db files out of git
├── golanging/          Go version
│   ├── main.go         the whole program (one source file)
│   ├── main_test.go    tests
│   ├── go.mod
│   └── go.sum
└── rust/               Rust version
    ├── README.md       Rust-specific notes
    ├── messages.txt    sample conversation, read by the Rust version (same content)
    ├── Cargo.toml
    ├── Cargo.lock
    └── src/
        ├── db.rs       Message struct and database functions
        └── main.rs     threads, channel and command loop
```

## Running the programs

### Go

Requires **Go 1.26 or newer**. The SQLite driver, `modernc.org/sqlite`, is pure Go, so no C compiler is needed. The first build downloads it.

```
cd golanging
go run .                   # reads ../messages.txt, starts a fresh chat.db, then the command loop
go run . -keep             # keep messages from earlier runs
go run . -file other.txt   # read a different conversation file
go run . -db other.db      # use a different database file
go test -v .               # run the tests
```

### Rust

Requires **Rust (stable)** from [rustup](https://rustup.rs/) and a **C compiler**, because `rusqlite`'s `bundled` feature compiles SQLite from source.

```
cd rust
cargo run --quiet          # reads messages.txt in the rust folder
```

Run it from inside `rust`, since that's where it looks for `messages.txt`.

## What both versions do

1. Read the conversation file: one `user: message` per line. Empty lines and lines starting with `#` are skipped, and only the first `:` separates the name from the text.
2. Start one concurrent sender per user (a goroutine in Go, an OS thread in Rust). Each sends its own lines in file order, so different users' messages interleave differently on every run.
3. Send every message through a channel to **one** worker that owns the database writes, so SQLite never sees two writers at once and never reports "database is locked".
4. Save each message with the sender's user ID and a timestamp.
5. Deliberately fail on bob's empty line (`bob:`), so the demo shows a database error being handled.
6. Open an interactive command loop for history, filter-by-user and keyword search.

```
alice ─┐
bob   ─┼─► channel ─► one database worker ─► SQLite
carol ─┘
```

## The conversation file

```
# lines starting with # and empty lines are ignored
alice: hey everyone, are we still on for rust tonight?
carol: reminder: demo freeze is tonight
bob:
```

The sample has 101 lines. 100 are saved, and `bob:` is rejected on purpose.

## Commands

| Action | Go | Rust |
|---|---|---|
| Show every message | `history` | `history` |
| Messages from one user | `user <name>` | `filter <name>` |
| Messages containing a word | `search <word>` | `search <word>` |
| Send a new message | `send <name> <text>` | — |
| List commands | `help` | `help` |
| Exit | `quit` / `exit` | `quit` / `exit` (Ctrl+C also works) |

## How each version is built

### Go (`golanging/main.go`)

| Section | Owner | Contents |
|---|---|---|
| Message | Pradeep | `Message` struct (`ID`, `UserID`, `Text`, `SentAt`) |
| Database | Pradeep | `openDB`, `saveMessage`, `history`, `filterByUser`, `searchKeyword` |
| Message handling | Amar | `simulateUser` and `send` (user goroutines → channel), `saveLoop` (the single goroutine that writes) |
| Main program | Amar | `loadConversation`, `main`, `runCommands` |

- **Writes** go through the channel to `saveLoop`. **Reads** (history, filter, search) call the shared `*sql.DB` directly, because `database/sql` is safe to use from several goroutines. `db.SetMaxOpenConns(1)` keeps it to a single SQLite connection.
- A `sync.WaitGroup` counts messages that have been sent but not yet saved. The command loop waits on it, so a query always sees everything sent so far.
- Go has no rule that closes a channel automatically. `main` calls `close(msgs)` itself, which ends `saveLoop`'s `for m := range in` loop.
- The table rejects empty text with `CHECK (message <> '')`.
- The database is a file, `chat.db`. Each run starts fresh unless you pass `-keep`.

### Rust (`rust/src/`)

- `db.rs` (Kenny) holds the `Message` struct and `init_db`, `save_message`, `history`, `filter_by_user`, `search_by_keyword`. Each function borrows a `Connection` and returns a `Result`.
- `main.rs` (Sasha) holds the `Command` enum, the handler thread, one thread per user, `load_messages` and the command loop.
- The handler thread **owns the only `Connection`**, and every save **and** every query goes through it. `rusqlite::Connection` isn't `Sync`, so the compiler won't let threads share it. Queries carry a one-shot reply channel so the command loop can wait for the answer.
- The channel closes by itself once every `Sender` is dropped. `main` also sends `Command::Shutdown`.
- bob's empty line is sent as `None`, which becomes SQL `NULL` and breaks the `NOT NULL` rule.
- The database is in memory (`Connection::open_in_memory`), so nothing is left on disk after the program exits.

## Differences between the two versions

These are worth knowing for the demo, and some are good material for the comparison.

| | Go | Rust |
|---|---|---|
| Source files | 1 (`main.go`) | 2 (`db.rs`, `main.rs`) |
| SQLite library | `database/sql` + `modernc.org/sqlite` (no C compiler) | `rusqlite` with `bundled` (needs a C compiler) |
| Where data is stored | file `chat.db` | in memory |
| Who runs queries | any goroutine, through the shared `*sql.DB` | only the handler thread, through reply channels |
| `sent_at` | set by the sender (`time.Now()`), stored as RFC 3339 UTC | set by SQLite (`DEFAULT strftime(...)`), UTC |
| Keyword search | substring, ignores case (`search is` also matches "this") | **whole words**, ignores case (`search is` doesn't match "this") |
| Empty message rejected by | `CHECK (message <> '')` | `NOT NULL` (sent as `NULL`) |
| Filter command name | `user <name>` | `filter <name>` |
| Unknown user or a line without `:` | stops with the line number | line without `:` is skipped; any user name is accepted |
| Delay between messages | random 0–50 ms before each | fixed 50 ms after each |
| Output line | `[15:04:05.000] #12 bob: text` | `[2026-10-03 15:04:05.123] bob: text` |
| Conversation file | `../messages.txt` (repo root) | `rust/messages.txt` |
| Automated tests | 6 (`go test -v .`) | none yet |

## Language comparison notes

- **Error handling.** Go returns errors as ordinary values. `saveMessage(db, m)` still compiles if you ignore the error, and nothing warns you. Rust's `Result` is marked `#[must_use]`, so ignoring one gives a warning. To ignore it on purpose you have to write `let _ = ...`, which shows up in the code (see `let _ = reply.send(...)` in `main.rs`).
- **Concurrency safety.** In Go, it's up to the programmer to share the database safely; the compiler doesn't check. In Rust, `Connection` isn't `Sync`, so giving it to exactly one thread isn't just a choice: the program won't compile any other way.
- **Closing channels.** In Go, `close(msgs)` has to be called by hand. In Rust, the channel closes when the last `Sender` is dropped.
- **Goroutines vs threads.** Goroutines are cheap and scheduled by the Go runtime. Rust's `thread::spawn` creates an OS thread.
- **Database libraries.** `database/sql` is a standard interface, and the driver is imported only for its side effect (`_ "modernc.org/sqlite"`). `rusqlite` is SQLite-specific.
- **Iteration errors.** In Go, `rows.Next()` returns false both at the end of the results and on an error, so `rows.Err()` has to be checked. In Rust, `query_map(...).collect::<Result<Vec<_>>>()` stops at the first error and returns it.
- **Strictness.** Unused imports and variables are compile errors in Go and warnings in Rust. Rust's ownership rules reject more programs at compile time.
