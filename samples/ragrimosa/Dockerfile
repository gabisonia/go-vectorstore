FROM golang:1.24-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/ragrimosa ./samples/ragrimosa

FROM alpine:3.20
RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=build /out/ragrimosa /usr/local/bin/ragrimosa

ENTRYPOINT ["/usr/local/bin/ragrimosa"]
