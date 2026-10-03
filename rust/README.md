# Simple Chat Application — Rust

Rust implementation for MSCS-632 Deliverable: a shared chat room with
simulated users, backed by SQLite, with a live command loop. No network and
no GUI.

## Features

- Three simulated users (`alice`, `bob`, `carol`) created at startup.
- The conversation (100 messages) is read from `messages.txt`, one
  `user: message` per line, so it can be edited without touching the code.
- Each user posts its messages into the shared room concurrently, on its
  own OS thread, with a short pause after each one so the users take turns.
- Every message is saved to a SQLite `messages` table (`user_id`, `message`,
  `sent_at`).
- After the initial burst of messages, the program enters an interactive
  command loop and stays alive until you stop it:
  - `history` — show every saved message
  - `filter <user>` — show messages from one user
  - `search <keyword>` — show messages containing a keyword
  - `help` — list commands
  - `quit` / `exit` — stop the program (Ctrl+C / Ctrl+D also work)
- A no-match filter or search prints "No messages found." instead of an
  empty, ambiguous list.
- A deliberately invalid send (the empty `bob:` line in `messages.txt`, sent
  as a `NULL` message body) demonstrates a real `rusqlite` error flowing back
  as a `Result` the caller must handle.
- Keyword search matches whole words, so `search is` does not match
  "finished" or "this".

## Structure

The code is split into two files, one per task assignment:

- `src/db.rs` (Kenny): the `Message` struct and the SQLite functions
  (`init_db`, `save_message`, `history`, `filter_by_user`,
  `search_by_keyword`).
- `src/main.rs` (Sasha): the `Command` enum, the handler thread, the
  simulated user threads, `load_messages()` which reads the conversation
  file, and the interactive loop in `main()`/`run_cli()`.
- `messages.txt`: the sample conversation.

`main.rs` pulls in the database code with `mod db;`.

## Design

The handler thread owns the only `rusqlite::Connection` for the program's
entire lifetime. Simulated users (and the CLI) never touch the database
directly — they only hold a `Sender<Command>` and talk to the handler over a
`std::sync::mpsc` channel, with a fresh one-shot reply channel per query.
This isn't just a style choice:

- `rusqlite::Connection` isn't `Sync`, so a single owner is the only way the
  compiler allows the database to be shared across threads at all.
- SQLite itself only allows one writer at a time — if several threads opened
  their own connection and wrote directly, concurrent inserts could fail
  with "database is locked." Funneling every save *and* every query through
  one thread's connection means there is never more than one writer, by
  construction, not by convention.

`main` spawns the handler, spawns one thread per simulated user, joins them,
then hands control to `run_cli()` on the main thread, which blocks on stdin
and answers `history`/`filter`/`search` against the live database until
`quit`, EOF, or Ctrl+C.

## Build & run

Requires Rust (stable) via [rustup](https://rustup.rs/) and a C compiler
(`cc`/`clang`) for the bundled SQLite.

```sh
cargo run --quiet
```

Run it from this `rust` folder, because the program looks for
`messages.txt` in the folder it is started from.

## Dependencies

- `rusqlite` (with the `bundled` feature, so it builds its own SQLite and
  doesn't need a system `libsqlite3`) — Go's equivalent is `database/sql`
  plus a SQLite driver package.
