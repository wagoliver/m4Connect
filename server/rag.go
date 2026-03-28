package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// ── Types ─────────────────────────────────────────────────────────────────────

type RAGResult struct {
	ConversationID string  `json:"conversation_id"`
	MessageID      string  `json:"message_id"`
	ChunkText      string  `json:"chunk_text"`
	Score          float64 `json:"score"`
	ConvTitle      string  `json:"conv_title"`
}

// ── Cosine similarity ─────────────────────────────────────────────────────────
// Pure Go — auto-vectorised on ARM64 (M4) by the compiler.

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		fa, fb := float64(a[i]), float64(b[i])
		dot += fa * fb
		na += fa * fa
		nb += fb * fb
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ── SearchRAG ─────────────────────────────────────────────────────────────────
// Loads all embeddings, scores against the query vector, returns top-K.

func SearchRAG(store *ConvStore, query []float32, limit int) ([]RAGResult, error) {
	all, err := store.AllEmbeddings()
	if err != nil {
		return nil, err
	}
	results := make([]RAGResult, 0, len(all))
	for _, e := range all {
		results = append(results, RAGResult{
			ConversationID: e.ConvID,
			MessageID:      e.MsgID,
			ChunkText:      e.ChunkText,
			Score:          cosine(query, e.Vector),
			ConvTitle:      e.ConvTitle,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// ── FormatRAGContext ──────────────────────────────────────────────────────────
// Formats top-K results as a plain-text block for system prompt injection.
// Filters out results with score < 0.3 (low relevance).

func FormatRAGContext(results []RAGResult) string {
	if len(results) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Contexto de conversas anteriores relevantes:\n\n")
	added := 0
	for i, r := range results {
		if r.Score < 0.30 {
			continue
		}
		title := r.ConvTitle
		if title == "" {
			title = "conversa anterior"
		}
		sb.WriteString(fmt.Sprintf("[%d] (de: %s)\n%s\n\n", i+1, title, r.ChunkText))
		added++
	}
	if added == 0 {
		return ""
	}
	return sb.String()
}
