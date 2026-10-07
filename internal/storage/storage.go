package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sources (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	peer_id INTEGER NOT NULL DEFAULT 0,
	access_hash INTEGER NOT NULL DEFAULT 0,
	url TEXT NOT NULL UNIQUE,
	username TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	is_private INTEGER NOT NULL DEFAULT 0,
	enabled INTEGER NOT NULL DEFAULT 1,
	status TEXT NOT NULL DEFAULT 'pending',
	last_error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	activated_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
	telegram_chat_id INTEGER NOT NULL,
	telegram_message_id INTEGER NOT NULL,
	grouped_id INTEGER NOT NULL DEFAULT 0,
	text TEXT NOT NULL DEFAULT '',
	media_json BLOB NOT NULL DEFAULT '[]',
	published_at INTEGER NOT NULL,
	received_at INTEGER NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	attempts INTEGER NOT NULL DEFAULT 0,
	next_attempt_at INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	classification_json BLOB NOT NULL DEFAULT '{}',
	destination_messages_json BLOB NOT NULL DEFAULT '[]',
	UNIQUE(telegram_chat_id, telegram_message_id)
);
CREATE INDEX IF NOT EXISTS idx_messages_queue ON messages(status, next_attempt_at, received_at);
CREATE INDEX IF NOT EXISTS idx_messages_group ON messages(source_id, grouped_id);

CREATE TABLE IF NOT EXISTS telegram_update_state (
	user_id INTEGER PRIMARY KEY,
	pts INTEGER NOT NULL,
	qts INTEGER NOT NULL,
	date INTEGER NOT NULL,
	seq INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS telegram_channel_state (
	user_id INTEGER NOT NULL,
	channel_id INTEGER NOT NULL,
	pts INTEGER NOT NULL,
	access_hash INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(user_id, channel_id)
);

CREATE TABLE IF NOT EXISTS telegram_user_hashes (
	user_id INTEGER NOT NULL,
	target_user_id INTEGER NOT NULL,
	access_hash INTEGER NOT NULL,
	PRIMARY KEY(user_id, target_user_id)
);

CREATE TABLE IF NOT EXISTS fingerprints (
	message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
	text_hash TEXT NOT NULL DEFAULT '',
	text_simhash INTEGER NOT NULL DEFAULT 0,
	image_hash INTEGER NOT NULL DEFAULT 0,
	media_unique TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_fingerprints_created ON fingerprints(created_at);
CREATE INDEX IF NOT EXISTS idx_fingerprints_exact ON fingerprints(text_hash, media_unique);

CREATE TABLE IF NOT EXISTS quarantine (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	message_id INTEGER NOT NULL UNIQUE REFERENCES messages(id) ON DELETE CASCADE,
	reason TEXT NOT NULL,
	confidence REAL NOT NULL DEFAULT 0,
	evidence_json BLOB NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL,
	resolved_at INTEGER,
	resolution TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_quarantine_open ON quarantine(resolved_at, created_at);

CREATE TABLE IF NOT EXISTS topics (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS feedback_examples (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	text TEXT NOT NULL,
	label TEXT NOT NULL,
	topic TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_feedback_label ON feedback_examples(label, created_at DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS feedback_fts USING fts5(
	text,
	content='feedback_examples',
	content_rowid='id',
	tokenize='unicode61'
);
CREATE TRIGGER IF NOT EXISTS feedback_ai AFTER INSERT ON feedback_examples BEGIN
	INSERT INTO feedback_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS feedback_ad AFTER DELETE ON feedback_examples BEGIN
	INSERT INTO feedback_fts(feedback_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;

CREATE TABLE IF NOT EXISTS publications (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
	destination_chat_id INTEGER NOT NULL,
	destination_message_id INTEGER NOT NULL,
	mode TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	UNIQUE(destination_chat_id, destination_message_id)
);
`

type Store struct {
	db *sql.DB
}

const (
	databaseOpenTimeout = 20 * time.Second
	maxFeedbackTerms    = 8
)

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), databaseOpenTimeout)
	defer cancel()
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) PutSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, time.Now().Unix())
	return err
}

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	return value, err
}

func (s *Store) AddSource(ctx context.Context, source Source) (int64, error) {
	now := time.Now().Unix()
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO sources(peer_id, access_hash, url, username, title, is_private, enabled, status, last_error, created_at, activated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET username=excluded.username, title=excluded.title, enabled=1
		RETURNING id`,
		source.PeerID, source.AccessHash, source.URL, source.Username, source.Title,
		source.Private, source.Enabled, defaultString(source.Status, "pending"), source.LastError,
		now, unixOrZero(source.ActivatedAt)).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) UpdateSourceResolved(ctx context.Context, id, peerID, accessHash int64, title, username string, private bool) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sources SET peer_id=?, access_hash=?, title=?, username=?, is_private=?, status='active',
			last_error='', activated_at=? WHERE id=?`,
		peerID, accessHash, title, username, private, time.Now().Unix(), id)
	return err
}

func (s *Store) UpdateSourceError(ctx context.Context, id int64, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sources SET status='error', last_error=? WHERE id=?`, message, id)
	return err
}

func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, peer_id, access_hash, url, username, title, is_private, enabled, status, last_error, created_at, activated_at
		FROM sources ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []Source

	for rows.Next() {
		var item Source
		var created, activated int64
		if err := rows.Scan(&item.ID, &item.PeerID, &item.AccessHash, &item.URL, &item.Username, &item.Title,
			&item.Private, &item.Enabled, &item.Status, &item.LastError, &created, &activated); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(created, 0)
		if activated > 0 {
			item.ActivatedAt = time.Unix(activated, 0)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) SourceByPeerID(ctx context.Context, peerID int64) (Source, error) {
	var item Source
	var created, activated int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, peer_id, access_hash, url, username, title, is_private, enabled, status, last_error, created_at, activated_at
		FROM sources WHERE peer_id=? AND enabled=1`, peerID).
		Scan(&item.ID, &item.PeerID, &item.AccessHash, &item.URL, &item.Username, &item.Title,
			&item.Private, &item.Enabled, &item.Status, &item.LastError, &created, &activated)
	if err != nil {
		return Source{}, err
	}
	item.CreatedAt = time.Unix(created, 0)
	if activated > 0 {
		item.ActivatedAt = time.Unix(activated, 0)
	}
	return item, nil
}

func (s *Store) SourceByID(ctx context.Context, id int64) (Source, error) {
	var item Source
	var created, activated int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, peer_id, access_hash, url, username, title, is_private, enabled, status, last_error, created_at, activated_at
		FROM sources WHERE id=?`, id).
		Scan(&item.ID, &item.PeerID, &item.AccessHash, &item.URL, &item.Username, &item.Title,
			&item.Private, &item.Enabled, &item.Status, &item.LastError, &created, &activated)
	if err != nil {
		return Source{}, err
	}
	item.CreatedAt = time.Unix(created, 0)
	if activated > 0 {
		item.ActivatedAt = time.Unix(activated, 0)
	}
	return item, nil
}

func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sources WHERE id=?`, id)
	return err
}

func (s *Store) EnqueueMessage(ctx context.Context, message Message) (int64, error) {
	media := message.Media
	if len(media) == 0 {
		media = json.RawMessage("[]")
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO messages(source_id, telegram_chat_id, telegram_message_id, grouped_id, text, media_json,
			published_at, received_at, status, next_attempt_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0)
		ON CONFLICT(telegram_chat_id, telegram_message_id) DO NOTHING`,
		message.SourceID, message.TelegramChatID, message.TelegramMsgID, message.GroupedID, message.Text,
		media, unixOrNow(message.PublishedAt), unixOrNow(message.ReceivedAt))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) QueueDepth(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM messages WHERE status IN ('pending','retry','processing')`).Scan(&count)
	return count, err
}

func (s *Store) ClaimNextMessage(ctx context.Context) (*Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `
		SELECT id, source_id, telegram_chat_id, telegram_message_id, grouped_id, text, media_json,
			published_at, received_at, status, attempts, next_attempt_at, last_error, classification_json,
			destination_messages_json
		FROM messages
		WHERE status IN ('pending','retry') AND next_attempt_at <= ?
		ORDER BY received_at ASC LIMIT 1`, time.Now().Unix())
	item, err := scanMessage(row)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE messages SET status='processing', attempts=attempts+1
		WHERE id=? AND status IN ('pending','retry')`, item.ID)
	if err != nil {
		return nil, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return nil, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	item.Status = "processing"
	item.Attempts++
	return item, nil
}

func (s *Store) ClaimNextMessages(ctx context.Context, albumSettleDelay time.Duration) ([]Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var id, sourceID, groupedID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id, source_id, grouped_id FROM messages
		WHERE status IN ('pending','retry') AND next_attempt_at <= ?
			AND (grouped_id=0 OR received_at <= ?)
		ORDER BY received_at ASC LIMIT 1`,
		time.Now().Unix(), time.Now().Add(-albumSettleDelay).Unix()).Scan(&id, &sourceID, &groupedID)
	if err != nil {
		return nil, err
	}
	var rows *sql.Rows
	if groupedID != 0 {
		rows, err = tx.QueryContext(ctx, `
			SELECT id, source_id, telegram_chat_id, telegram_message_id, grouped_id, text, media_json,
				published_at, received_at, status, attempts, next_attempt_at, last_error, classification_json,
				destination_messages_json
			FROM messages WHERE source_id=? AND grouped_id=? AND status IN ('pending','retry')
			ORDER BY telegram_message_id`, sourceID, groupedID)
	} else {
		rows, err = tx.QueryContext(ctx, `
			SELECT id, source_id, telegram_chat_id, telegram_message_id, grouped_id, text, media_json,
				published_at, received_at, status, attempts, next_attempt_at, last_error, classification_json,
				destination_messages_json FROM messages WHERE id=?`, id)
	}
	if err != nil {
		return nil, err
	}
	messages, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, sql.ErrNoRows
	}
	var result sql.Result
	if groupedID != 0 {
		result, err = tx.ExecContext(ctx, `
			UPDATE messages SET status='processing', attempts=attempts+1
			WHERE source_id=? AND grouped_id=? AND status IN ('pending','retry')`, sourceID, groupedID)
	} else {
		result, err = tx.ExecContext(ctx, `
			UPDATE messages SET status='processing', attempts=attempts+1
			WHERE id=? AND status IN ('pending','retry')`, id)
	}
	if err != nil {
		return nil, err
	}
	changed, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return nil, rowsErr
	}
	if changed != int64(len(messages)) {
		return nil, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for index := range messages {
		messages[index].Status = "processing"
		messages[index].Attempts++
	}
	return messages, nil
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer func() { _ = rows.Close() }()
	var messages []Message

	for rows.Next() {
		item, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, *item)
	}
	return messages, rows.Err()
}

func (s *Store) GetMessage(ctx context.Context, id int64) (*Message, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, source_id, telegram_chat_id, telegram_message_id, grouped_id, text, media_json,
			published_at, received_at, status, attempts, next_attempt_at, last_error, classification_json,
			destination_messages_json FROM messages WHERE id=?`, id)
	return scanMessage(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessage(row rowScanner) (*Message, error) {
	var item Message
	var published, received, next int64
	var media, classification, destinations []byte
	if err := row.Scan(&item.ID, &item.SourceID, &item.TelegramChatID, &item.TelegramMsgID, &item.GroupedID,
		&item.Text, &media, &published, &received, &item.Status, &item.Attempts, &next,
		&item.LastError, &classification, &destinations); err != nil {
		return nil, err
	}
	item.Media = json.RawMessage(media)
	item.Classification = json.RawMessage(classification)
	item.DestinationMsgs = json.RawMessage(destinations)
	item.PublishedAt = time.Unix(published, 0)
	item.ReceivedAt = time.Unix(received, 0)
	if next > 0 {
		item.NextAttemptAt = time.Unix(next, 0)
	}
	return &item, nil
}

func (s *Store) MarkMessage(ctx context.Context, id int64, status string, classification, destinations json.RawMessage) error {
	if len(classification) == 0 {
		classification = json.RawMessage("{}")
	}
	if len(destinations) == 0 {
		destinations = json.RawMessage("[]")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE messages SET status=?, classification_json=?, destination_messages_json=?, last_error=''
		WHERE id=?`, status, classification, destinations, id)
	return err
}

func (s *Store) RetryMessage(ctx context.Context, id int64, errText string, after time.Duration) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE messages SET status='retry', last_error=?, next_attempt_at=? WHERE id=?`,
		errText, time.Now().Add(after).Unix(), id)
	return err
}

func (s *Store) AddFingerprint(ctx context.Context, fp Fingerprint) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO fingerprints(message_id, text_hash, text_simhash, image_hash, media_unique, created_at)
		VALUES(?, ?, ?, ?, ?, ?)`,
		fp.MessageID, fp.TextHash, int64(fp.TextSimHash), int64(fp.ImageHash), fp.MediaUnique, unixOrNow(fp.CreatedAt))
	return err
}

func (s *Store) RecentFingerprints(ctx context.Context, since time.Time) ([]Fingerprint, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT message_id, text_hash, text_simhash, image_hash, media_unique, created_at
		FROM fingerprints WHERE created_at >= ? ORDER BY created_at DESC`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []Fingerprint

	for rows.Next() {
		var fp Fingerprint
		var sim, image, created int64
		if err := rows.Scan(&fp.MessageID, &fp.TextHash, &sim, &image, &fp.MediaUnique, &created); err != nil {
			return nil, err
		}
		fp.TextSimHash, fp.ImageHash, fp.CreatedAt = uint64(sim), uint64(image), time.Unix(created, 0)
		result = append(result, fp)
	}
	return result, rows.Err()
}

func (s *Store) AddQuarantine(ctx context.Context, messageID int64, reason string, confidence float64, evidence []string) error {
	data, _ := json.Marshal(evidence)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO quarantine(message_id, reason, confidence, evidence_json, created_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(message_id) DO UPDATE SET reason=excluded.reason, confidence=excluded.confidence,
			evidence_json=excluded.evidence_json`,
		messageID, reason, confidence, data, time.Now().Unix())
	return err
}

func (s *Store) ListQuarantine(ctx context.Context, limit, offset int) ([]QuarantineItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT q.id, q.message_id, COALESCE(s.title, s.username, s.url), m.text, q.reason, q.confidence,
			q.evidence_json, q.created_at, q.resolved_at, q.resolution, s.url, m.telegram_message_id,
			m.classification_json
		FROM quarantine q
		JOIN messages m ON m.id=q.message_id JOIN sources s ON s.id=m.source_id
		ORDER BY q.resolved_at IS NULL DESC, q.created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []QuarantineItem

	for rows.Next() {
		var item QuarantineItem
		var created int64
		var resolved sql.NullInt64
		var evidence, classification []byte
		if err := rows.Scan(&item.ID, &item.MessageID, &item.SourceTitle, &item.Text, &item.Reason,
			&item.Confidence, &evidence, &created, &resolved, &item.Resolution, &item.SourceURL,
			&item.TelegramMsgID, &classification); err != nil {
			return nil, err
		}
		item.Evidence = json.RawMessage(evidence)
		item.Classification = json.RawMessage(classification)
		item.CreatedAt = time.Unix(created, 0)
		if resolved.Valid {
			value := time.Unix(resolved.Int64, 0)
			item.ResolvedAt = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ResolveQuarantine(ctx context.Context, id int64, resolution string) error {
	if resolution != "false_positive" && resolution != "confirmed_ad" {
		return errors.New("invalid quarantine resolution")
	}
	return withTx(ctx, s.db, func(tx *sql.Tx) error {
		var messageID int64
		var text string
		if err := tx.QueryRowContext(ctx, `
			SELECT q.message_id, m.text FROM quarantine q JOIN messages m ON m.id=q.message_id WHERE q.id=?`, id).
			Scan(&messageID, &text); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE quarantine SET resolved_at=?, resolution=? WHERE id=?`, time.Now().Unix(), resolution, id); err != nil {
			return err
		}
		label := "ad"
		if resolution == "false_positive" {
			label = "not_ad"
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO feedback_examples(text, label, topic, source, created_at) VALUES(?, ?, '', 'admin', ?)`,
			text, label, time.Now().Unix())
		return err
	})
}

func (s *Store) AddFeedback(ctx context.Context, example FeedbackExample) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO feedback_examples(text, label, topic, source, created_at) VALUES(?, ?, ?, ?, ?)`,
		example.Text, example.Label, example.Topic, example.Source, time.Now().Unix())
	return err
}

func (s *Store) SearchFeedback(ctx context.Context, text string, limit int) ([]FeedbackExample, error) {
	terms := strings.Fields(strings.ToLower(text))
	if len(terms) > maxFeedbackTerms {
		terms = terms[:maxFeedbackTerms]
	}
	var rows *sql.Rows
	var err error
	if len(terms) == 0 {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, text, label, topic, source, created_at FROM feedback_examples
			ORDER BY created_at DESC LIMIT ?`, limit)
	} else {
		for i := range terms {
			terms[i] = `"` + strings.ReplaceAll(terms[i], `"`, "") + `"`
		}
		rows, err = s.db.QueryContext(ctx, `
			SELECT f.id, f.text, f.label, f.topic, f.source, f.created_at
			FROM feedback_fts x JOIN feedback_examples f ON f.id=x.rowid
			WHERE feedback_fts MATCH ? ORDER BY bm25(feedback_fts) LIMIT ?`, strings.Join(terms, " OR "), limit)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []FeedbackExample

	for rows.Next() {
		var item FeedbackExample
		var created int64
		if err := rows.Scan(&item.ID, &item.Text, &item.Label, &item.Topic, &item.Source, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(created, 0)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ListTopics(ctx context.Context) ([]Topic, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, enabled, created_at FROM topics ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []Topic

	for rows.Next() {
		var item Topic
		var created int64
		if err := rows.Scan(&item.ID, &item.Name, &item.Enabled, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(created, 0)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) AddTopic(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO topics(name, enabled, created_at) VALUES(?, 1, ?)
		ON CONFLICT(name) DO UPDATE SET enabled=1`, strings.TrimSpace(name), time.Now().Unix())
	return err
}

func (s *Store) DeleteTopic(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM topics WHERE id=?`, id)
	return err
}

func (s *Store) AddPublication(ctx context.Context, messageID, chatID int64, targetMessageID int, mode string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO publications(message_id, destination_chat_id, destination_message_id, mode, created_at)
		VALUES(?, ?, ?, ?, ?)`, messageID, chatID, targetMessageID, mode, time.Now().Unix())
	return err
}

func (s *Store) Cleanup(ctx context.Context, before time.Time) (int64, error) {
	var affected int64
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM fingerprints WHERE created_at < ?`, before.Unix())
		if err != nil {
			return err
		}
		affected, _ = result.RowsAffected()
		_, err = tx.ExecContext(ctx, `
			DELETE FROM messages WHERE received_at < ? AND status IN ('published','duplicate','ignored')
			AND id NOT IN (SELECT message_id FROM quarantine)`, before.Unix())
		return err
	})
	return affected, err
}

func withTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func unixOrNow(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().Unix()
	}
	return t.Unix()
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
