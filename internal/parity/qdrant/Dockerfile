FROM golang:1.26-bookworm AS tests
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY vectordata vectordata
COPY internal internal
COPY stores stores
RUN go test -race -tags=qdrantparity -c -o /native.test ./stores/qdrant

FROM mcr.microsoft.com/dotnet/sdk:10.0.400
WORKDIR /src/internal/parity/qdrant
COPY internal/parity/qdrant/Reference.csproj internal/parity/qdrant/packages.lock.json ./
RUN dotnet restore --locked-mode
COPY internal/parity/qdrant/Program.cs ./
RUN dotnet build --no-restore
COPY --from=tests /native.test /native.test
WORKDIR /src/stores/qdrant
ENTRYPOINT ["/native.test", "-test.v", "-test.timeout=5m"]
