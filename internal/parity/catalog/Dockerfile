FROM golang:1.25-bookworm AS tests
ARG provider
WORKDIR /src
COPY go.mod go.sum ./
COPY vectordata vectordata
COPY internal internal
COPY stores stores
RUN go test -race -tags=referenceparity -c -o /reference.test ./internal/parity/catalog/$provider

FROM mcr.microsoft.com/dotnet/sdk:10.0.400
ARG provider
WORKDIR /src/$provider
COPY internal/parity/catalog/Directory.Build.props /src/
COPY internal/parity/catalog/$provider/Reference.csproj internal/parity/catalog/$provider/packages.lock.json ./
RUN dotnet restore --locked-mode
COPY internal/parity/catalog/ArticleFixture.cs /src/
COPY internal/parity/catalog/$provider/Program.cs ./
RUN dotnet publish --no-restore -o /app
COPY --from=tests /reference.test /app/reference.test
WORKDIR /app
ENTRYPOINT ["/app/reference.test", "-test.v"]
