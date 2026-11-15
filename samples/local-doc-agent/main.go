// local-doc-agent is a local agent/tools/retrieval demo.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func run(ctx context.Context, args []string, in io.Reader, out, trace io.Writer) error {
	if len(args) == 0 || args[0] == "help" {
		fmt.Fprintln(out, "Usage: doc-agent {ask QUESTION | chat | ingest}")
		return nil
	}
	if args[0] != "ask" && args[0] != "chat" && args[0] != "ingest" {
		return fmt.Errorf("unknown command %q", args[0])
	}
	if args[0] == "ask" && len(args) < 2 {
		return fmt.Errorf("ask requires a question")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	records, err := readCorpus(cfg.Docs)
	if err != nil {
		return err
	}
	embedder := &localEmbedder{URL: cfg.URL, Model: cfg.EmbedModel, Dimensions: cfg.Dimensions, Client: &http.Client{Timeout: cfg.Timeout}}
	setup, stop := context.WithTimeout(ctx, cfg.Timeout)
	i, err := openIndex(setup, cfg, embedder)
	if err != nil {
		stop()
		return err
	}
	defer i.store.Close()
	changed, err := i.sync(setup, records)
	stop()
	if err != nil {
		return err
	}
	fmt.Fprintf(trace, "Indexed %d chunks (%d new/changed); SQLite persists between runs.\n", len(records), changed)
	if args[0] == "ingest" {
		return nil
	}
	a, state, err := newAgent(cfg, i, trace)
	if err != nil {
		return err
	}
	session, err := a.CreateSession(ctx)
	if err != nil {
		return err
	}
	turn := func(question string) error {
		request, cancel := context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
		return ask(request, a, state, session, question, out)
	}
	if args[0] == "ask" {
		return turn(strings.Join(args[1:], " "))
	}
	fmt.Fprintln(out, "Ask about the bundled documentation. Type reset for a new conversation, or exit to quit.")
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 8192)
	for {
		fmt.Fprint(out, "\nYou> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		question := strings.TrimSpace(scanner.Text())
		if question == "exit" || question == "quit" {
			return nil
		}
		if question == "reset" {
			session, err = a.CreateSession(ctx)
			if err != nil {
				return err
			}
			state.mu.Lock()
			state.known = map[string]bool{}
			state.mu.Unlock()
			fmt.Fprintln(out, "New conversation; document index retained.")
			continue
		}
		if question == "" {
			continue
		}
		if err := turn(question); err != nil {
			return err
		}
	}
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
