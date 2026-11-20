// The shared sample AppHost replaces the repository's separate orchestration
// files. Choose one mode per run; it never starts paid OpenAI calls implicitly.
package main

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"apphost/modules/aspire"
)

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func main() {
	mode := env("VECTORSTORE_SAMPLE", "services")
	if mode != "services" && mode != "semantic-search" && mode != "ragrimosa" && mode != "local-doc-agent" && mode != "docs-retrieval-pilot" {
		log.Fatal("VECTORSTORE_SAMPLE must be services, semantic-search, ragrimosa, local-doc-agent or docs-retrieval-pilot")
	}
	builder, err := aspire.CreateBuilder()
	if err != nil {
		log.Fatal(aspire.FormatError(err))
	}
	if mode == "local-doc-agent" {
		addAgent(builder)
	} else if mode == "docs-retrieval-pilot" {
		addPilot(builder)
	} else {
		addPostgres(builder, mode)
	}
	if mode == "services" {
		port := portEnv("DEV_REDIS_PORT", 63799)
		// Keep the local demo's existing unauthenticated connection contract.
		builder.AddRedis("redis").WithPassword(nil).WithoutHttpsCertificate().WithImageTag("8.2").WithHostPort(&port).
			WithModule("/usr/local/lib/redis/modules/rejson.so").
			WithModule("/usr/local/lib/redis/modules/redisearch.so").
			WithLifetime(aspire.ContainerLifetimePersistent).
			WithVolume("/data", aspire.VolumeName("go-vectorstore_redis-data")).
			WithArgs([]string{"--appendonly", "yes", "--appendfsync", "everysec", "--maxmemory-policy", "noeviction"})
		version := "v1.20.2"
		builder.AddContainer("qdrant", &aspire.AddContainerOptions{Image: "qdrant/qdrant", Tag: &version}).
			WithHttpEndpoint(aspire.HttpEndpointName("http"), aspire.HttpEndpointTargetPort(6333), aspire.HttpEndpointPort(portEnv("DEV_QDRANT_PORT", 63339))).
			WithHttpHealthCheck(aspire.HttpHealthCheckPath("/readyz")).
			WithLifetime(aspire.ContainerLifetimePersistent).
			WithVolume("/qdrant/storage", aspire.VolumeName("go-vectorstore-dev-qdrant")).
			WithEnvironment("QDRANT__TELEMETRY_DISABLED", "true").WithEnvironment("QDRANT__CLUSTER__ENABLED", "false")
	}
	app, err := builder.Build()
	if err != nil {
		log.Fatal(aspire.FormatError(err))
	}
	if err := app.Run(); err != nil {
		log.Fatal(aspire.FormatError(err))
	}
}

func addPilot(builder aspire.DistributedApplicationBuilder) {
	directory, err := filepath.Abs(filepath.Join("..", "docs-retrieval-pilot"))
	if err != nil {
		log.Fatal(err)
	}
	model := env("PILOT_EMBED_MODEL", "all-minilm:22m")
	version := "0.40.16"
	ollama := builder.AddContainer("pilot-ollama", &aspire.AddContainerOptions{Image: "ollama/ollama", Tag: &version}).
		WithHttpEndpoint(aspire.HttpEndpointName("http"), aspire.HttpEndpointTargetPort(11434)).
		WithHttpHealthCheck(aspire.HttpHealthCheckPath("/api/tags")).
		WithLifetime(aspire.ContainerLifetimePersistent).
		WithVolume("/root/.ollama", aspire.VolumeName(env("PILOT_MODEL_VOLUME", "vectorstore-docs-pilot_models"))).
		WithContainerRuntimeArgs([]string{"--cpus=2", "--memory=1g"}).
		WithEnvironment("OLLAMA_HOST", "0.0.0.0:11434").WithEnvironment("OLLAMA_NO_CLOUD", "1").
		WithEnvironment("OLLAMA_NUM_PARALLEL", "1").WithEnvironment("OLLAMA_MAX_LOADED_MODELS", "1")
	// Only the embedding model is needed; do not initialize the agent's chat model.
	models := builder.AddContainer("pilot-models", &aspire.AddContainerOptions{Image: "ollama/ollama", Tag: &version}).
		WithEntrypoint("/bin/sh").
		WithArgs([]string{"-ec", `ollama show "$EMBED_MODEL" >/dev/null 2>&1 || ollama pull "$EMBED_MODEL"`}).
		WithEnvironment("OLLAMA_HOST", ollama.GetEndpoint("http")).
		WithEnvironment("EMBED_MODEL", model).WaitFor(ollama)
	builder.AddGoApp("docs-retrieval-pilot", directory).
		WithAppArgs([]any{
			"-ollama", ollama.GetEndpoint("http"),
			"-model", model, "-dimensions", env("PILOT_EMBED_DIMENSIONS", "384"),
			"-corpus", env("PILOT_CORPUS", filepath.Join(directory, "..", "..", "docs")),
			"-queries", env("PILOT_QUERIES", filepath.Join(directory, "queries.json")),
			"-output", env("PILOT_OUTPUT", filepath.Join(directory, "data", "pilot")),
			"-revision", env("PILOT_REVISION", "unspecified"),
			"-concurrency", env("PILOT_CONCURRENCY", "4"),
			"-requests-per-worker", env("PILOT_REQUESTS_PER_WORKER", "25"),
		}).WaitForCompletion(models)
}

func portEnv(name string, fallback int) int {
	value, err := strconv.Atoi(env(name, strconv.Itoa(fallback)))
	if err != nil || value < 1 || value > 65535 {
		log.Fatalf("%s must be a port between 1 and 65535", name)
	}
	return value
}

func addPostgres(builder aspire.DistributedApplicationBuilder, mode string) {
	// Fixed development credentials keep the existing native sample DSNs valid.
	// Aspire still models the password as a secret parameter and owns readiness.
	user := builder.AddParameter("postgres-user", aspire.AddParameterValue("postgres"))
	password := builder.AddParameter("postgres-password", aspire.AddParameterValue("postgres"), aspire.AddParameterSecret(true))
	pg := builder.AddPostgres("postgres", aspire.AddPostgresUserName(user), aspire.AddPostgresPassword(password)).
		WithImage("pgvector/pgvector").WithImageTag("pg16").
		WithLifetime(aspire.ContainerLifetimePersistent).
		WithVolume("/var/lib/postgresql/data", aspire.VolumeName("go-vectorstore-dev-postgres"))
	if mode == "services" {
		port := portEnv("DEV_POSTGRES_PORT", 54329)
		pg.WithHostPort(&port)
	}
	db := pg.AddDatabase("vectorstore-db", aspire.AddDatabaseDatabaseName("vectorstore_test"))
	if mode == "services" {
		return
	}
	key := builder.AddParameter("openai-key", aspire.AddParameterValue(os.Getenv("OPENAI_API_KEY")), aspire.AddParameterSecret(true))
	query := env("QUERY", "How can I reduce cloud costs without hurting reliability?")
	args := []string{"-q", query}
	if mode == "semantic-search" {
		if category := os.Getenv("CATEGORY"); category != "" {
			args = append(args, "-category", category)
		}
	} else {
		query = env("QUERY", "How did Lacrimosa and Rex build trust, and what happened later in Amsterdam?")
		args = []string{"-q", query, "-topk", env("TOP_K", "4")}
	}
	app := builder.AddGoApp(mode, filepath.Join("..", mode)).WithArgs(args).
		WithReference(db).WaitFor(db).
		WithEnvironment("PG_DSN", "").
		WithEnvironment("PGHOST", pg.Host()).WithEnvironment("PGPORT", pg.Port()).
		WithEnvironment("PGUSER", pg.UserNameReference()).WithEnvironment("PGPASSWORD", password).
		WithEnvironment("PGDATABASE", "vectorstore_test").WithEnvironment("PGSSLMODE", "disable").
		WithEnvironment("OPENAI_API_KEY", key).
		WithEnvironment("OPENAI_EMBEDDING_MODEL", env("OPENAI_EMBEDDING_MODEL", "text-embedding-3-small")).
		WithEnvironment("OPENAI_BASE_URL", env("OPENAI_BASE_URL", "https://api.openai.com/v1")).
		WithExplicitStart()
	if mode == "ragrimosa" {
		app.WithEnvironment("OPENAI_CHAT_MODEL", env("OPENAI_CHAT_MODEL", "gpt-4o-mini"))
	}
	// Starting the AppHost only starts its database. Run the application via
	// the resource's Start action after deliberately configuring credentials.
}

func addAgent(builder aspire.DistributedApplicationBuilder) {
	command := env("AGENT_COMMAND", "ask")
	if command != "ask" && command != "chat" && command != "ingest" {
		log.Fatal("AGENT_COMMAND must be ask, chat or ingest")
	}
	directory, err := filepath.Abs(filepath.Join("..", "local-doc-agent"))
	if err != nil {
		log.Fatal(err)
	}
	version := "0.40.16"
	ollama := builder.AddContainer("ollama", &aspire.AddContainerOptions{Image: "ollama/ollama", Tag: &version}).
		WithHttpEndpoint(aspire.HttpEndpointName("http"), aspire.HttpEndpointTargetPort(11434)).
		WithHttpHealthCheck(aspire.HttpHealthCheckPath("/api/tags")).
		WithLifetime(aspire.ContainerLifetimePersistent).
		WithVolume("/root/.ollama", aspire.VolumeName(env("AGENT_MODEL_VOLUME", "vector-doc-agent_models"))).
		WithEnvironment("OLLAMA_HOST", "0.0.0.0:11434").WithEnvironment("OLLAMA_NO_CLOUD", "1").
		WithEnvironment("OLLAMA_CONTEXT_LENGTH", "4096").WithEnvironment("OLLAMA_NUM_PARALLEL", "1").WithEnvironment("OLLAMA_MAX_LOADED_MODELS", "2")
	models := builder.AddContainer("models", &aspire.AddContainerOptions{Image: "ollama/ollama", Tag: &version}).
		WithEntrypoint("/bin/sh").WithArgs([]string{"/setup-models.sh"}).
		WithBindMount(filepath.Join(directory, "setup-models.sh"), "/setup-models.sh", aspire.BindMountIsReadOnly(true)).
		WithEnvironment("OLLAMA_HOST", ollama.GetEndpoint("http")).
		WithEnvironment("SOURCE_CHAT_MODEL", env("CHAT_MODEL", "qwen3:4b-instruct")).
		WithEnvironment("MODEL_THREADS", env("MODEL_THREADS", "4")).
		WithEnvironment("EMBED_MODEL", env("EMBED_MODEL", "all-minilm:22m")).WaitFor(ollama)
	args := []string{command}
	if command == "ask" {
		args = append(args, env("QUERY", "Can SQLite do hybrid search? What should I use instead? Cite the documentation."))
	}
	agent := builder.AddDockerfile("agent", directory).WithArgs(args).
		WithEnvironment("OLLAMA_URL", ollama.GetEndpoint("http")).
		WithEnvironment("CHAT_MODEL", "doc-agent-chat").
		WithEnvironment("EMBED_MODEL", env("EMBED_MODEL", "all-minilm:22m")).
		WithEnvironment("EMBED_DIMENSIONS", env("EMBED_DIMENSIONS", "384")).
		WithEnvironment("REQUEST_TIMEOUT", env("REQUEST_TIMEOUT", "5m")).
		WithEnvironment("DOCS_DIR", "/docs").WithEnvironment("DATABASE_PATH", "/data/documents.db").
		WithEnvironment("AGENT_FRAMEWORK_USER_AGENT_DISABLED", "true").
		WithBindMount(filepath.Join(directory, "docs"), "/docs", aspire.BindMountIsReadOnly(true)).
		WithVolume("/data", aspire.VolumeName(env("AGENT_INDEX_VOLUME", "vector-doc-agent_index"))).
		WaitForCompletion(models)
	if command == "chat" {
		agent.WithTerminal()
	}
}
