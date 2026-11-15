package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	URL, ChatModel, EmbedModel, Docs, Database string
	Dimensions                                 int
	Timeout                                    time.Duration
}

func loadConfig() (config, error) {
	env := func(key, fallback string) string {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
		return fallback
	}
	dimensions, err := strconv.Atoi(env("EMBED_DIMENSIONS", "384"))
	if err != nil || dimensions < 1 || dimensions > 4096 {
		return config{}, fmt.Errorf("EMBED_DIMENSIONS must be between 1 and 4096")
	}
	timeout, err := time.ParseDuration(env("REQUEST_TIMEOUT", "5m"))
	if err != nil || timeout <= 0 || timeout > 15*time.Minute {
		return config{}, fmt.Errorf("REQUEST_TIMEOUT must be positive and at most 15m")
	}
	endpoint := strings.TrimRight(env("OLLAMA_URL", "http://localhost:11434"), "/")
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return config{}, fmt.Errorf("OLLAMA_URL must be an HTTP(S) origin without credentials/query/fragment")
	}
	return config{URL: endpoint, ChatModel: env("CHAT_MODEL", "qwen3:4b-instruct"), EmbedModel: env("EMBED_MODEL", "all-minilm:22m"), Docs: env("DOCS_DIR", "docs"), Database: env("DATABASE_PATH", "data/documents.db"), Dimensions: dimensions, Timeout: timeout}, nil
}
