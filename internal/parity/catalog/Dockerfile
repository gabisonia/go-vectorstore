FROM golang:1.25-bookworm AS tests
ARG provider
WORKDIR /src
COPY go.mod go.sum ./
COPY vectordata vectordata
COPY internal internal
COPY stores stores
WORKDIR /src/internal/parity/catalog/$provider
RUN go test -race -tags=referenceparity -c -o /reference.test .

FROM mcr.microsoft.com/dotnet/sdk:10.0.400 AS reference
ARG provider
ARG reference_dir=$provider
WORKDIR /src/$provider
COPY internal/parity/catalog/Directory.Build.props /src/
COPY internal/parity/catalog/$reference_dir/Reference.csproj internal/parity/catalog/$reference_dir/packages.lock.json ./
RUN dotnet restore --locked-mode
COPY internal/parity/catalog/ArticleFixture.cs /src/
COPY internal/parity/catalog/$reference_dir/Program.cs ./
RUN dotnet publish --no-restore -o /app

FROM reference
COPY --from=tests /reference.test /app/reference.test
WORKDIR /app
ENTRYPOINT ["/app/reference.test", "-test.v"]
