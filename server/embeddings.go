package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// ── EmbedQueue ────────────────────────────────────────────────────────────────
// Async worker that fetches embeddings from Ollama and persists them.
// Non-blocking: if the channel is full, the job is silently dropped.

type embedJob struct {
	convID string
	msgID  string
	text   string
}

type EmbedQueue struct {
	store     *ConvStore
	ollamaURL string
	model     string
	ch        chan embedJob
}

func NewEmbedQueue(store *ConvStore, ollamaURL, model string) *EmbedQueue {
	q := &EmbedQueue{
		store:     store,
		ollamaURL: ollamaURL,
		model:     model,
		ch:        make(chan embedJob, 64),
	}
	go q.worker()
	return q
}

// Submit enqueues a text chunk for embedding. Returns immediately; never blocks.
func (q *EmbedQueue) Submit(convID, msgID, text string) {
	if text == "" {
		return
	}
	select {
	case q.ch <- embedJob{convID: convID, msgID: msgID, text: text}:
	default:
		// channel full — drop gracefully
	}
}

// Len returns the current queue depth (useful for monitoring).
func (q *EmbedQueue) Len() int { return len(q.ch) }

func (q *EmbedQueue) worker() {
	for job := range q.ch {
		vec, err := q.fetchEmbedding(job.text)
		if err != nil {
			log.Printf("embed: fetch error: %v", err)
			continue
		}
		if err := q.store.SaveEmbedding(job.convID, job.msgID, job.text, vec, q.model); err != nil {
			log.Printf("embed: save error: %v", err)
		}
	}
}

// fetchEmbedding calls Ollama /api/embeddings and returns the float32 vector.
func (q *EmbedQueue) fetchEmbedding(text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]string{
		"model":  q.model,
		"prompt": text,
	})
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(q.ollamaURL+"/api/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embeddings: status %d", resp.StatusCode)
	}
	var result struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding returned")
	}
	return result.Embedding, nil
}
