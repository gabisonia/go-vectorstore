FROM golang:1.22-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/semantic-search ./samples/semantic-search

FROM alpine:3.20
RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=build /out/semantic-search /usr/local/bin/semantic-search

ENTRYPOINT ["/usr/local/bin/semantic-search"]
