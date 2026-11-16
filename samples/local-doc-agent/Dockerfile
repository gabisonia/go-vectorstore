FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/doc-agent .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 demo && mkdir /data && chown demo:demo /data
WORKDIR /app
COPY --from=build /out/doc-agent /app/doc-agent
USER demo
ENTRYPOINT ["/app/doc-agent"]
