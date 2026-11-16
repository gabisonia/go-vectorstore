package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAgentToolsAndPersistentIndex(t *testing.T) {
	var calls, embedded, repairStage atomic.Int32
	var premature, skipSearch, wantHistory, alwaysBad atomic.Bool
	var final atomic.Value
	final.Store("SQLite does not support hybrid search [storage.md#sqlite].")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		if r.URL.Path == "/api/embed" {
			var body struct {
				Input    []string
				Truncate bool
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			if body.Truncate {
				t.Error("embedding truncation enabled")
			}
			embedded.Add(int32(len(body.Input)))
			vectors := make([][]float32, len(body.Input))
			for i := range vectors {
				vectors[i] = []float32{1, 0, 0, 0}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unmodeled endpoint", 501)
			return
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		n := (calls.Add(1) - 1) % 3
		if premature.Load() {
			switch repairStage.Add(1) {
			case 1:
				n = 0
			case 2:
				n = 2
			case 3:
				n = 1
				if !bytes.Contains(body["tool_choice"], []byte("read_section")) {
					t.Error("repair did not require inspection through framework")
				}
			case 4:
				n = 2
			default:
				t.Error("unbounded correction loop")
			}
		}
		if skipSearch.Load() {
			switch repairStage.Add(1) {
			case 1:
				n = 2
			case 2:
				n = 0
			case 3:
				n = 1
			case 4:
				n = 2
			default:
				t.Error("unbounded search correction")
			}
		}
		if alwaysBad.Load() {
			n = 2
		}
		if wantHistory.Load() && calls.Load() == 4 && !bytes.Contains(body["messages"], []byte("question one")) {
			t.Error("framework did not retain conversational context")
		}
		if wantHistory.Load() && calls.Load() == 7 && bytes.Contains(body["messages"], []byte("question one")) {
			t.Error("reset did not clear conversation history")
		}
		var message map[string]any
		reason := "tool_calls"
		switch n {
		case 0:
			if !bytes.Contains(body["tool_choice"], []byte("search_docs")) {
				t.Error("initial search was not required")
			}
			message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "search", "type": "function", "function": map[string]any{"name": "search_docs", "arguments": `{"query":"SQLite hybrid search"}`}}}}
		case 1:
			if !bytes.Contains(body["messages"], []byte("storage.md#sqlite")) {
				t.Error("search results not fed back to model")
			}
			if bytes.Contains(body["tool_choice"], []byte("search_docs")) {
				t.Error("framework did not reset required tool mode")
			}
			message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "read", "type": "function", "function": map[string]any{"name": "read_section", "arguments": `{"section_id":"storage.md#sqlite"}`}}}}
		case 2:
			if !alwaysBad.Load() && !(skipSearch.Load() && repairStage.Load() == 1) && !bytes.Contains(body["messages"], []byte("SQLite does not support hybrid search")) {
				t.Error("section content not fed back to model")
			}
			reason = "stop"
			message = map[string]any{"role": "assistant", "content": final.Load().(string)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("chat-%d", calls.Load()), "object": "chat.completion", "created": 1, "model": "fixture", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": reason}}})
	}))
	defer server.Close()
	docs := t.TempDir()
	if err := os.WriteFile(filepath.Join(docs, "storage.md"), []byte("# Storage\n\n## SQLite\nSQLite does not support hybrid search. Use PostgreSQL instead.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLLAMA_URL", server.URL)
	t.Setenv("EMBED_DIMENSIONS", "4")
	t.Setenv("CHAT_MODEL", "fixture")
	t.Setenv("DOCS_DIR", docs)
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "index.db"))
	for round := 0; round < 2; round++ {
		before := embedded.Load()
		var out, trace bytes.Buffer
		if err := run(context.Background(), []string{"ask", "Does SQLite support hybrid?"}, strings.NewReader(""), &out, &trace); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Verified source IDs: storage.md#sqlite") || !strings.Contains(trace.String(), "[tool] search_docs") || !strings.Contains(trace.String(), "[tool] read_section") {
			t.Fatalf("no real framework/tool flow: answer=%s trace=%s", &out, &trace)
		}
		if round == 1 && (embedded.Load()-before != 1 || !strings.Contains(trace.String(), "0 new/changed")) {
			t.Fatalf("reopened index unnecessarily embedded documents: %s", &trace)
		}
	}
	if calls.Load() != 6 {
		t.Fatalf("expected two three-call agent loops, got %d", calls.Load())
	}
	for _, answer := range []string{"Invented source [private.md#secret].", "An answer without citations."} {
		final.Store(answer)
		var output bytes.Buffer
		err := run(context.Background(), []string{"ask", "question"}, strings.NewReader(""), &output, &bytes.Buffer{})
		if err == nil || output.Len() != 0 {
			t.Fatalf("accepted unverified answer: %q error=%v", output.String(), err)
		}
	}
	premature.Store(true)
	final.Store("SQLite does not support hybrid search [storage.md#sqlite].")
	var repaired, repairTrace bytes.Buffer
	if err := run(context.Background(), []string{"ask", "question"}, strings.NewReader(""), &repaired, &repairTrace); err != nil {
		t.Fatal(err)
	}
	if repairStage.Load() != 4 || !strings.Contains(repairTrace.String(), "[policy] source inspection required") || !strings.Contains(repaired.String(), "Verified source IDs:") {
		t.Fatalf("inspection correction was not bounded and verified: %d %s %s", repairStage.Load(), &repairTrace, &repaired)
	}

	premature.Store(false)
	skipSearch.Store(true)
	repairStage.Store(0)
	repaired.Reset()
	repairTrace.Reset()
	if err := run(context.Background(), []string{"ask", "follow-up"}, strings.NewReader(""), &repaired, &repairTrace); err != nil {
		t.Fatal(err)
	}
	if repairStage.Load() != 4 || !strings.Contains(repairTrace.String(), "[policy] new question requires search_docs") {
		t.Fatalf("skipped search was not corrected: %s", &repairTrace)
	}
	skipSearch.Store(false)
	wantHistory.Store(true)
	calls.Store(0)
	var conversation bytes.Buffer
	if err := run(context.Background(), []string{"chat"}, strings.NewReader("question one\nquestion two\nreset\nquestion three\nexit\n"), &conversation, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(conversation.String(), "Verified source IDs:") != 3 {
		t.Fatalf("follow-up conversation: %s", &conversation)
	}
	wantHistory.Store(false)
	alwaysBad.Store(true)
	calls.Store(0)
	var rejected bytes.Buffer
	if err := run(context.Background(), []string{"ask", "question"}, strings.NewReader(""), &rejected, &bytes.Buffer{}); err == nil || calls.Load() != 4 || rejected.Len() != 0 {
		t.Fatalf("model refusal was not bounded: calls=%d output=%s error=%v", calls.Load(), &rejected, err)
	}

}
