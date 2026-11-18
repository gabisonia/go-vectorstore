package main

import (
	"apphost/modules/aspire"
	"log"
)

func main() {
	builder, err := aspire.CreateBuilder()
	if err != nil {
		log.Fatal(aspire.FormatError(err))
	}
	// Aspire owns credentials, ports, health checks and startup ordering. The
	// database container/network and its volume survive AppHost restarts.
	// pgvector's "pg18" tag is not a numeric PostgreSQL tag, so set the
	// PostgreSQL 18+ parent mount explicitly instead of inferring its layout.
	postgres := builder.AddPostgres("postgres").
		WithImage("pgvector/pgvector").WithImageTag("pg18").
		WithLifetime(aspire.ContainerLifetimePersistent).
		WithVolume("/var/lib/postgresql", aspire.VolumeName("go-vectorstore-aspire17-data"))
	database := postgres.AddDatabase("vectorstore")
	builder.AddGoApp("vector-api", "./api").
		WithReference(database).WaitFor(database).
		WithEnvironment("PGHOST", postgres.Host()).
		WithEnvironment("PGPORT", postgres.Port()).
		WithEnvironment("PGUSER", postgres.UserNameReference()).
		WithEnvironment("PGPASSWORD", postgres.PasswordParameter()).
		WithEnvironment("PGDATABASE", "vectorstore").
		WithEnvironment("PGSSLMODE", "disable").
		WithHttpEndpoint(aspire.HttpEndpointEnv("PORT")).
		WithHttpHealthCheck(aspire.HttpHealthCheckPath("/health")).
		WithExternalHttpEndpoints().
		WithOtlpExporter()
	application, err := builder.Build()
	if err != nil {
		log.Fatal(aspire.FormatError(err))
	}
	if err := application.Run(); err != nil {
		log.Fatal(aspire.FormatError(err))
	}
}
