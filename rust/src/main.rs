//! Threads, channel and main program.
//!
//! This file holds the concurrency side of the chat app: the `Command` enum,
//! the handler thread that owns the database connection, the simulated user
//! threads, and the interactive command loop.
//!
//! The `Message` struct and the database functions live in `db.rs`.

mod db;

use db::{filter_by_user, history, init_db, save_message, search_by_keyword, Message};
use rusqlite::{Connection, Result};
use std::io::{self, BufRead, Write};
use std::sync::mpsc;
use std::thread;
use std::time::Duration;

fn print_query(label: &str, result: Result<Vec<Message>>) {
    println!("\n-- {} --", label);
    match result {
        Ok(messages) if messages.is_empty() => println!("No messages found."),
        Ok(messages) => {
            for m in messages {
                println!("[{}] {}: {}", m.sent_at, m.user_id, m.message);
            }
        }
        Err(e) => println!("Error: {}", e),
    }
}

/// Everything the handler thread can be asked to do. `Save` comes from the
/// simulated user threads; `History`/`Filter`/`Search` come from the
/// interactive CLI on the main thread and carry a one-shot reply channel
/// (plain `std::sync::mpsc`, used once per request) so the caller can block
/// on the answer.
enum Command {
    Save {
        user_id: String,
        message: Option<String>,
    },
    History {
        reply: mpsc::Sender<Result<Vec<Message>>>,
    },
    Filter {
        user_id: String,
        reply: mpsc::Sender<Result<Vec<Message>>>,
    },
    Search {
        keyword: String,
        reply: mpsc::Sender<Result<Vec<Message>>>,
    },
    Shutdown,
}

/// The handler owns the only `rusqlite::Connection` for the program's whole
/// lifetime. That's not just tidy design: SQLite allows only one writer at a
/// time, so if several threads opened their own connection and wrote
/// directly, some inserts would fail with "database is locked." Funneling
/// every save AND every query through one thread's connection sidesteps
/// that entirely — there is never more than one writer, by construction.
fn run_handler(rx: mpsc::Receiver<Command>) {
    let conn = Connection::open_in_memory().expect("failed to open in-memory database");
    init_db(&conn).expect("failed to create messages table");

    for cmd in rx {
        match cmd {
            Command::Save { user_id, message } => match save_message(&conn, &user_id, message.as_deref()) {
                Ok(()) => println!(
                    "saved: {} -> {}",
                    user_id,
                    message.as_deref().unwrap_or("")
                ),
                Err(e) => println!("Error saving message from {}: {}", user_id, e),
            },
            Command::History { reply } => {
                let _ = reply.send(history(&conn));
            }
            Command::Filter { user_id, reply } => {
                let _ = reply.send(filter_by_user(&conn, &user_id));
            }
            Command::Search { keyword, reply } => {
                let _ = reply.send(search_by_keyword(&conn, &keyword));
            }
            Command::Shutdown => break,
        }
    }
}

/// One simulated user. It sends its messages one at a time and pauses after
/// each, the way a person takes a moment between messages. The pause is what
/// lets the three users take turns, so the chat reads as a conversation
/// instead of one user finishing before the next one starts.
fn simulate_user(tx: mpsc::Sender<Command>, user_id: &str, messages: Vec<Option<&str>>) {
    for message in messages {
        let _ = tx.send(Command::Save {
            user_id: user_id.to_string(),
            message: message.map(str::to_string),
        });
        thread::sleep(Duration::from_millis(300));
    }
}

/// Sends a query command with a fresh one-shot reply channel and blocks for
/// the handler's answer. If the handler is somehow gone, that's treated as
/// "no results" rather than a panic — the CLI should never crash under the
/// user just because a query raced shutdown.
fn query_blocking(
    tx: &mpsc::Sender<Command>,
    make_cmd: impl FnOnce(mpsc::Sender<Result<Vec<Message>>>) -> Command,
) -> Result<Vec<Message>> {
    let (reply_tx, reply_rx) = mpsc::channel();
    let _ = tx.send(make_cmd(reply_tx));
    reply_rx.recv().unwrap_or_else(|_| Ok(Vec::new()))
}

fn print_help() {
    println!(
        "commands:\n  history            show every saved message\n  filter <user>      show messages from one user\n  search <keyword>   show messages containing a keyword\n  help               show this list\n  quit | exit        stop the program"
    );
}

fn print_prompt() {
    print!("> ");
    let _ = io::stdout().flush();
}

/// The CLI loop that keeps the program "alive until interrupted": it blocks
/// on stdin and answers queries against the live database until the user
/// types `quit`/`exit`, pipes EOF, or hits Ctrl+C.
fn run_cli(tx: &mpsc::Sender<Command>) {
    print_prompt();
    for line in io::stdin().lock().lines() {
        let line = match line {
            Ok(l) => l,
            Err(_) => break,
        };
        let line = line.trim();
        if line.is_empty() {
            print_prompt();
            continue;
        }

        let mut parts = line.splitn(2, ' ');
        let command = parts.next().unwrap_or("");
        let arg = parts.next().unwrap_or("").trim();

        match command {
            "history" => print_query("full history", query_blocking(tx, |reply| Command::History { reply })),
            "filter" if !arg.is_empty() => print_query(
                &format!("messages from {}", arg),
                query_blocking(tx, |reply| Command::Filter {
                    user_id: arg.to_string(),
                    reply,
                }),
            ),
            "search" if !arg.is_empty() => print_query(
                &format!("keyword search: '{}'", arg),
                query_blocking(tx, |reply| Command::Search {
                    keyword: arg.to_string(),
                    reply,
                }),
            ),
            "filter" => println!("usage: filter <user>"),
            "search" => println!("usage: search <keyword>"),
            "help" => print_help(),
            "quit" | "exit" => break,
            other => println!("unknown command: '{}' (type 'help' for the list)", other),
        }
        print_prompt();
    }
    println!();
}

fn main() {
    let (tx, rx) = mpsc::channel::<Command>();
    let handler = thread::spawn(move || run_handler(rx));

    println!("-- sending messages --");

    let seed: Vec<(&str, Vec<Option<&str>>)> = vec![
        (
            "alice",
            vec![
                Some("hey everyone, are we still on for rust tonight?"),
                Some("I finished the database functions this morning"),
                Some("can someone test the search command?"),
                Some("see you all at 7"),
            ],
        ),
        (
            "bob",
            vec![
                Some("yep, I'll be there"),
                Some("the go version is almost done too"),
                None, // deliberately bad: violates the NOT NULL message column
                Some("I will test search after lunch"),
                Some("rust was harder than go for me"),
            ],
        ),
        (
            "carol",
            vec![
                Some("reminder: demo freeze is tonight"),
                Some("slides are due before the demo"),
                Some("I can test the filter command"),
                Some("good work everyone, see you tonight"),
            ],
        ),
    ];

    let senders: Vec<_> = seed
        .into_iter()
        .map(|(user_id, messages)| {
            let tx = tx.clone();
            thread::spawn(move || simulate_user(tx, user_id, messages))
        })
        .collect();

    for handle in senders {
        handle.join().expect("a user thread panicked");
    }

    // Ask the handler for the history and wait for the answer. The reply can
    // only come back after every save above has been processed, so all the
    // "saved:" lines are printed before the prompt appears.
    let _ = query_blocking(&tx, |reply| Command::History { reply });

    println!("\nInitial messages sent. Type 'help' for commands, 'quit' to stop.");
    run_cli(&tx);

    let _ = tx.send(Command::Shutdown);
    // Drop main's own Sender so the channel closes if Shutdown didn't already
    // break the handler's loop (e.g. stdin hit EOF before `quit`).
    drop(tx);

    handler.join().expect("handler thread panicked");
}
