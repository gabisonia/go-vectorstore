package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/postgres"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

//go:embed web/*
var web embed.FS

type application struct {
	documents *vectordata.TypedModelCollection[string, Document]
	ping      func(context.Context) error
	latency   metric.Float64Histogram
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("Sample stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	shutdownTelemetry, err := setupTelemetry(ctx)
	if err != nil {
		return err
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(shutdown); err != nil {
			slog.Error("Telemetry flush failed", "error", err)
		}
	}()
	// pgx reads the standard PG* environment injected by the AppHost. No fixed
	// ports, inline passwords or .NET connection-string parsing are needed.
	config, err := pgxpool.ParseConfig("")
	if err != nil {
		return fmt.Errorf("invalid PostgreSQL configuration")
	}
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	defer pool.Close()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := pool.Ping(startup); err != nil {
		return fmt.Errorf("connect to sample PostgreSQL: %w", err)
	}
	opts := postgres.DefaultStoreOptions()
	opts.Schema = "aspire_demo"
	opts.Embedder = fixtureEmbedder{}
	store, err := postgres.NewVectorStore(pool, opts)
	if err != nil {
		return err
	}
	documents, err := vectordata.EnsureTypedModelCollection[string, Document](startup, store, "documents", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	if _, err := documents.Upsert(startup, catalog); err != nil {
		return fmt.Errorf("seed catalog: %w", err)
	}
	slog.InfoContext(startup, "Catalog ready", "documents", len(catalog), "provider", "postgres", "embedding", "deterministic fixture", "dimensions", dimensions)
	app, err := newApplication(documents, pool.Ping)
	if err != nil {
		return err
	}
	host := os.Getenv("HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: app.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: time.Minute}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	slog.Info("Vector search ready", "address", listener.Addr().String())
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

func newApplication(documents *vectordata.TypedModelCollection[string, Document], ping func(context.Context) error) (*application, error) {
	latency, err := otel.Meter("aspire-vector-search").Float64Histogram("vectorstore.search.duration", metric.WithUnit("s"), metric.WithDescription("Duration of vector and hybrid searches"))
	if err != nil {
		return nil, err
	}
	return &application{documents: documents, ping: ping, latency: latency}, nil
}

func (a *application) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := web.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if a.ping(ctx) != nil {
			http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
	})
	mux.HandleFunc("GET /api/status", a.status)
	mux.HandleFunc("GET /api/search", a.search)
	mux.HandleFunc("POST /api/documents", a.add)
	mux.HandleFunc("DELETE /api/documents/{id}", a.remove)
	return otelhttp.NewHandler(mux, "vector-api", otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/health" }))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func badRequest(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": message})
}
func failed(w http.ResponseWriter, r *http.Request, action string, err error) {
	slog.ErrorContext(r.Context(), action+" failed", "error", err)
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": action + " unavailable. Check vector-api logs in Aspire."})
}

func (a *application) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	count, err := a.documents.Count(ctx, nil)
	if err != nil {
		failed(w, r, "Status", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": "PostgreSQL / pgvector", "documents": count, "dimensions": dimensions, "embedding": "Deterministic fixture", "categories": categories})
}

type searchHit struct {
	Document  Document             `json:"document"`
	Score     float64              `json:"score"`
	Distance  *float64             `json:"distance,omitempty"`
	ScoreKind vectordata.ScoreKind `json:"scoreKind"`
}

func (a *application) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	category := r.URL.Query().Get("category")
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "vector"
	}
	top := 5
	if value := r.URL.Query().Get("top"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 10 {
			badRequest(w, "Top must be between 1 and 10.")
			return
		}
		top = parsed
	}
	if q == "" || len(q) > 256 {
		badRequest(w, "Enter a query of 1–256 bytes.")
		return
	}
	if category != "" && !slices.Contains(categories, category) {
		badRequest(w, "Choose a known category.")
		return
	}
	if mode != "vector" && mode != "hybrid" {
		badRequest(w, "Choose vector or hybrid search.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ctx, span := otel.Tracer("aspire-vector-search").Start(ctx, "vectorstore.search", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("db.system.name", "postgresql"), attribute.String("search.mode", mode), attribute.Int("search.top", top)))
	defer span.End()
	opts := vectordata.ModelSearchOptions{}
	if category != "" {
		opts.Filter = vectordata.Eq(vectordata.Column("Category"), category)
	}
	start := time.Now()
	var matches []vectordata.TypedModelSearchResult[Document]
	var err error
	if mode == "hybrid" {
		matches, err = a.documents.HybridSearchText(ctx, q, q, top, vectordata.HybridSearchOptions{ModelSearchOptions: opts})
	} else {
		matches, err = a.documents.SearchText(ctx, q, top, opts)
	}
	elapsed := time.Since(start)
	a.latency.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attribute.String("search.mode", mode)))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "search failed")
		failed(w, r.WithContext(ctx), "Search", err)
		return
	}
	hits := make([]searchHit, len(matches))
	for i, match := range matches {
		hits[i] = searchHit{Document: match.Item, Score: match.Score, ScoreKind: match.ScoreKind}
		if mode == "vector" {
			distance := match.Distance
			hits[i].Distance = &distance
		}
	}
	slog.InfoContext(ctx, "Search completed", "mode", mode, "category", category, "results", len(hits), "duration_ms", float64(elapsed.Microseconds())/1000)
	writeJSON(w, http.StatusOK, map[string]any{"results": hits, "mode": mode, "elapsedMs": float64(elapsed.Microseconds()) / 1000})
}

func (a *application) add(w http.ResponseWriter, r *http.Request) {
	var input struct{ Title, Category, Body string }
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		badRequest(w, "Send one JSON object with title, category and body.")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		badRequest(w, "Send exactly one JSON object.")
		return
	}
	input.Title, input.Body = strings.TrimSpace(input.Title), strings.TrimSpace(input.Body)
	if input.Title == "" || len(input.Title) > 160 || input.Body == "" || len(input.Body) > 4096 || !slices.Contains(categories, input.Category) {
		badRequest(w, "Use a title (1–160 bytes), body (1–4096 bytes), and known category.")
		return
	}
	id, err := vectordata.NewUUIDKey()
	if err != nil {
		failed(w, r, "Create", err)
		return
	}
	doc := Document{ID: "note-" + id, Title: input.Title, Category: input.Category, Body: input.Body}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, err := a.documents.Insert(ctx, []Document{doc}); err != nil {
		failed(w, r, "Create", err)
		return
	}
	slog.InfoContext(ctx, "Document added", "document_id", doc.ID, "category", doc.Category)
	writeJSON(w, http.StatusCreated, doc)
}

func (a *application) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !strings.HasPrefix(id, "note-") || len(id) > 64 {
		badRequest(w, "Only documents added in this demo can be removed.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	count, err := a.documents.Delete(ctx, []string{id})
	if err != nil {
		failed(w, r, "Delete", err)
		return
	}
	if count == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Document not found."})
		return
	}
	slog.InfoContext(ctx, "Document removed", "document_id", id)
	w.WriteHeader(http.StatusNoContent)
}
