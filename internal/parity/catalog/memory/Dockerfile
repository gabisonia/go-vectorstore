FROM golang:1.25-bookworm AS tests
WORKDIR /src
COPY go.mod go.sum ./
COPY vectordata vectordata
COPY internal internal
COPY stores stores
RUN go test -race -tags=memoryparity -c -o /memory.test ./internal/parity/catalog/memory

FROM mcr.microsoft.com/dotnet/sdk:10.0.400
WORKDIR /src/memory
COPY internal/parity/catalog/Directory.Build.props /src/
COPY internal/parity/catalog/memory/Reference.csproj internal/parity/catalog/memory/packages.lock.json ./
RUN dotnet restore --locked-mode
COPY internal/parity/catalog/memory/Program.cs ./
RUN dotnet publish --no-restore -o /app
COPY --from=tests /memory.test /app/memory.test
WORKDIR /app
ENTRYPOINT ["/app/memory.test", "-test.v"]
