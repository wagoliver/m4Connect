package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var inferenceClient = &http.Client{Timeout: 10 * time.Minute}

type InferenceRequest struct {
	Model    string         `json:"model"`
	Messages []OllamaChatMsg `json:"messages"`
}

type OllamaChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatChunk struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done               bool  `json:"done"`
	TotalDuration      int64 `json:"total_duration"`
	LoadDuration       int64 `json:"load_duration"`
	PromptEvalCount    int   `json:"prompt_eval_count"`
	PromptEvalDuration int64 `json:"prompt_eval_duration"`
	EvalCount          int   `json:"eval_count"`
	EvalDuration       int64 `json:"eval_duration"`
}

func handleInference(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req InferenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" || len(req.Messages) == 0 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   true,
	})

	resp, err := inferenceClient.Post(ollamaBase+"/api/chat", "application/json", bytes.NewReader(payload))
	if err != nil {
		sseWrite(w, flusher, "error", map[string]string{"msg": "Ollama indisponível"})
		return
	}
	defer resp.Body.Close()

	var (
		inThink bool
		buf     string
	)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var chunk ollamaChatChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}

		if chunk.Done {
			// Flush remaining buffer
			if buf != "" {
				evName := "token"
				if inThink {
					evName = "think"
				}
				sseWrite(w, flusher, evName, map[string]string{"token": buf})
			}
			if inThink {
				sseWrite(w, flusher, "think_end", nil)
			}

			var tps, promptTps float64
			if chunk.EvalDuration > 0 {
				tps = float64(chunk.EvalCount) / float64(chunk.EvalDuration) * 1e9
			}
			if chunk.PromptEvalDuration > 0 {
				promptTps = float64(chunk.PromptEvalCount) / float64(chunk.PromptEvalDuration) * 1e9
			}
			sseWrite(w, flusher, "stats", map[string]interface{}{
				"tps":               tps,
				"eval_count":        chunk.EvalCount,
				"prompt_eval_count": chunk.PromptEvalCount,
				"prompt_tps":        promptTps,
				"total_ms":          chunk.TotalDuration / 1_000_000,
				"load_ms":           chunk.LoadDuration / 1_000_000,
			})
			sseWrite(w, flusher, "done", nil)
			return
		}

		buf += chunk.Message.Content
		buf = flushThinkBuf(w, flusher, buf, &inThink)
	}
}

// flushThinkBuf processes the accumulated buffer, detecting <think>…</think> blocks.
// Returns the remaining unsafe (partial-tag) suffix.
func flushThinkBuf(w http.ResponseWriter, f http.Flusher, buf string, inThink *bool) string {
	for {
		if *inThink {
			if idx := strings.Index(buf, "</think>"); idx >= 0 {
				if idx > 0 {
					sseWrite(w, f, "think", map[string]string{"token": buf[:idx]})
				}
				buf = buf[idx+8:]
				*inThink = false
				sseWrite(w, f, "think_end", nil)
			} else {
				safe := holdBack(buf, "</think>")
				if safe > 0 {
					sseWrite(w, f, "think", map[string]string{"token": buf[:safe]})
					buf = buf[safe:]
				}
				return buf
			}
		} else {
			if idx := strings.Index(buf, "<think>"); idx >= 0 {
				if idx > 0 {
					sseWrite(w, f, "token", map[string]string{"token": buf[:idx]})
				}
				buf = buf[idx+7:]
				*inThink = true
				sseWrite(w, f, "think_start", nil)
			} else {
				safe := holdBack(buf, "<think>")
				if safe > 0 {
					sseWrite(w, f, "token", map[string]string{"token": buf[:safe]})
					buf = buf[safe:]
				}
				return buf
			}
		}
	}
}

// holdBack returns how many bytes can be safely emitted — holding back any
// suffix that could be the beginning of tag (to avoid splitting across chunks).
func holdBack(buf, tag string) int {
	for i := len(tag) - 1; i >= 1; i-- {
		if strings.HasSuffix(buf, tag[:i]) {
			return len(buf) - i
		}
	}
	return len(buf)
}

func sseWrite(w http.ResponseWriter, f http.Flusher, event string, data interface{}) {
	var d string
	if data == nil {
		d = "{}"
	} else {
		b, _ := json.Marshal(data)
		d = string(b)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, d)
	f.Flush()
}
