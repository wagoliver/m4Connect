package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// ── Domain types ──────────────────────────────────────────────────────────────

type Conversation struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Model     string `json:"model"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	MsgCount  int    `json:"msg_count"`
}

type Message struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	Role           string `json:"role"`
	Content        string `json:"content"`
	Position       int    `json:"position"`
	CreatedAt      int64  `json:"created_at"`
}

// ── ConvStore ─────────────────────────────────────────────────────────────────

type ConvStore struct {
	db *sql.DB
}

// newID generates a random UUID-like string.
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NewConvStore initialises the three conversation tables on the shared DB.
func NewConvStore(db *sql.DB) (*ConvStore, error) {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS conversations (
			id          TEXT PRIMARY KEY,
			title       TEXT NOT NULL,
			model       TEXT NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL,
			updated_at  INTEGER NOT NULL,
			msg_count   INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS messages (
			id              TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
			role            TEXT NOT NULL,
			content         TEXT NOT NULL,
			position        INTEGER NOT NULL,
			created_at      INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS embeddings (
			id              TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
			message_id      TEXT REFERENCES messages(id) ON DELETE CASCADE,
			chunk_text      TEXT NOT NULL,
			vector_blob     BLOB NOT NULL,
			dims            INTEGER NOT NULL,
			embed_model     TEXT NOT NULL DEFAULT 'nomic-embed-text',
			created_at      INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_messages_conv    ON messages(conversation_id);
		CREATE INDEX IF NOT EXISTS idx_embeddings_conv  ON embeddings(conversation_id);
	`)
	if err != nil {
		return nil, err
	}
	return &ConvStore{db: db}, nil
}

// ── Conversation CRUD ─────────────────────────────────────────────────────────

func (s *ConvStore) CreateConversation(title, model string) (Conversation, error) {
	now := time.Now().Unix()
	c := Conversation{
		ID: newID(), Title: title, Model: model,
		CreatedAt: now, UpdatedAt: now, MsgCount: 0,
	}
	_, err := s.db.Exec(
		`INSERT INTO conversations (id, title, model, created_at, updated_at, msg_count)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.Title, c.Model, c.CreatedAt, c.UpdatedAt, c.MsgCount,
	)
	return c, err
}

func (s *ConvStore) ListConversations() ([]Conversation, error) {
	rows, err := s.db.Query(
		`SELECT id, title, model, created_at, updated_at, msg_count
		 FROM conversations ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.Model, &c.CreatedAt, &c.UpdatedAt, &c.MsgCount); err != nil {
			continue
		}
		list = append(list, c)
	}
	return list, nil
}

func (s *ConvStore) GetConversation(id string) (*Conversation, error) {
	var c Conversation
	err := s.db.QueryRow(
		`SELECT id, title, model, created_at, updated_at, msg_count
		 FROM conversations WHERE id = ?`, id,
	).Scan(&c.ID, &c.Title, &c.Model, &c.CreatedAt, &c.UpdatedAt, &c.MsgCount)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *ConvStore) DeleteConversation(id string) error {
	_, err := s.db.Exec(`DELETE FROM conversations WHERE id = ?`, id)
	return err
}

func (s *ConvStore) UpdateTitle(id, title string) error {
	_, err := s.db.Exec(
		`UPDATE conversations SET title = ?, updated_at = ? WHERE id = ?`,
		title, time.Now().Unix(), id)
	return err
}

// ── Messages ──────────────────────────────────────────────────────────────────

func (s *ConvStore) SaveMessage(convID, msgID, role, content string, position int) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO messages (id, conversation_id, role, content, position, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		msgID, convID, role, content, position, now,
	)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE conversations SET updated_at = ?,
		 msg_count = (SELECT COUNT(*) FROM messages WHERE conversation_id = ?)
		 WHERE id = ?`,
		now, convID, convID,
	)
	return err
}

func (s *ConvStore) GetMessages(convID string) ([]Message, error) {
	rows, err := s.db.Query(
		`SELECT id, conversation_id, role, content, position, created_at
		 FROM messages WHERE conversation_id = ? ORDER BY position ASC`, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.Position, &m.CreatedAt); err != nil {
			continue
		}
		list = append(list, m)
	}
	return list, nil
}

// ── Embeddings ────────────────────────────────────────────────────────────────

// SaveEmbedding stores a float32 vector as a little-endian BLOB.
func (s *ConvStore) SaveEmbedding(convID, msgID, chunkText string, vector []float32, model string) error {
	blob := make([]byte, len(vector)*4)
	for i, v := range vector {
		binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(v))
	}
	_, err := s.db.Exec(
		`INSERT INTO embeddings
		 (id, conversation_id, message_id, chunk_text, vector_blob, dims, embed_model, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		newID(), convID, msgID, chunkText, blob, len(vector), model, time.Now().Unix(),
	)
	return err
}

type embeddingRow struct {
	ConvID    string
	MsgID     string
	ChunkText string
	Vector    []float32
	ConvTitle string
}

// AllEmbeddings loads every stored vector (used for in-memory cosine search).
func (s *ConvStore) AllEmbeddings() ([]embeddingRow, error) {
	rows, err := s.db.Query(`
		SELECT e.conversation_id, e.message_id, e.chunk_text, e.vector_blob,
		       COALESCE(c.title, '')
		FROM embeddings e
		LEFT JOIN conversations c ON c.id = e.conversation_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []embeddingRow
	for rows.Next() {
		var r embeddingRow
		var blob []byte
		if err := rows.Scan(&r.ConvID, &r.MsgID, &r.ChunkText, &blob, &r.ConvTitle); err != nil {
			continue
		}
		r.Vector = make([]float32, len(blob)/4)
		for i := range r.Vector {
			r.Vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
		}
		results = append(results, r)
	}
	return results, nil
}

func (s *ConvStore) CountEmbeddings() int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM embeddings`).Scan(&n)
	return n
}
