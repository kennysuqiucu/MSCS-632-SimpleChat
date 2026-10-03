//! Message struct and database functions.
//!
//! This file holds the data side of the chat app: the `Message` struct and
//! every function that talks to SQLite (create the table, save a message,
//! read the history, filter by user, search by keyword).
//!
//! Nothing here knows about threads or channels. Each function borrows a
//! `Connection` and returns a `Result`, so the caller decides what to do
//! when the database reports an error.

use rusqlite::{params, Connection, Result, Row};

/// One row of chat history.
#[derive(Debug)]
pub struct Message {
    pub user_id: String,
    pub message: String,
    pub sent_at: String,
}

/// Turns one database row into a `Message`.
fn row_to_message(row: &Row) -> Result<Message> {
    Ok(Message {
        user_id: row.get(0)?,
        message: row.get(1)?,
        sent_at: row.get(2)?,
    })
}

/// Makes an empty `messages` table.
///
/// The database is a file, so a table from an earlier run may still be in
/// it. That table is dropped first, which means every run starts with an
/// empty history and the ids start again at 1.
pub fn init_db(conn: &Connection) -> Result<()> {
    conn.execute("DROP TABLE IF EXISTS messages", [])?;
    conn.execute(
        "CREATE TABLE messages (
            id        INTEGER PRIMARY KEY AUTOINCREMENT,
            user_id   TEXT NOT NULL,
            message   TEXT NOT NULL,
            sent_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
        )",
        [],
    )?;
    Ok(())
}

/// `message: None` is accepted on purpose — it violates the NOT NULL column
/// and lets the demo show a real rusqlite error flowing back as a `Result`,
/// rather than a hand-rolled validation error.
pub fn save_message(conn: &Connection, user_id: &str, message: Option<&str>) -> Result<()> {
    conn.execute(
        "INSERT INTO messages (user_id, message) VALUES (?1, ?2)",
        params![user_id, message],
    )?;
    Ok(())
}

/// Returns every saved message, oldest first.
pub fn history(conn: &Connection) -> Result<Vec<Message>> {
    let mut stmt = conn.prepare("SELECT user_id, message, sent_at FROM messages ORDER BY id")?;
    let rows = stmt.query_map([], row_to_message)?.collect();
    rows
}

/// Returns only the messages sent by one user.
pub fn filter_by_user(conn: &Connection, user_id: &str) -> Result<Vec<Message>> {
    let mut stmt = conn.prepare(
        "SELECT user_id, message, sent_at FROM messages WHERE user_id = ?1 ORDER BY id",
    )?;
    let rows = stmt.query_map(params![user_id], row_to_message)?.collect();
    rows
}

/// Returns only the messages that contain the keyword as a whole word
/// (any letter case). Searching for "is" finds "freeze is tonight" but not
/// "finished" or "this".
///
/// It works in two steps. The SQL `LIKE` picks every message that contains
/// the letters anywhere, and then `has_word` keeps only the messages where
/// those letters are a word on their own.
pub fn search_by_keyword(conn: &Connection, keyword: &str) -> Result<Vec<Message>> {
    let keyword = keyword.to_lowercase();
    let pattern = format!("%{}%", keyword);
    let mut stmt = conn.prepare(
        "SELECT user_id, message, sent_at FROM messages WHERE LOWER(message) LIKE ?1 ORDER BY id",
    )?;
    let candidates: Result<Vec<Message>> = stmt.query_map(params![pattern], row_to_message)?.collect();

    let mut matches = Vec::new();
    for message in candidates? {
        if has_word(&message.message, &keyword) {
            matches.push(message);
        }
    }
    Ok(matches)
}

/// True when one of the words in `text` is exactly `keyword`.
/// The text is split wherever a character is not a letter, a digit or an
/// apostrophe, so "tonight?" is read as the word "tonight".
fn has_word(text: &str, keyword: &str) -> bool {
    let text = text.to_lowercase();
    for word in text.split(|c: char| !c.is_alphanumeric() && c != '\'') {
        if word == keyword {
            return true;
        }
    }
    false
}
