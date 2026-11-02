FROM golang:1.26.6 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mcp ./cmd/mcp

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 app
WORKDIR /app
COPY --from=build --chown=app:app /out/mcp ./mcp
USER app
EXPOSE 8081
CMD ["./mcp"]
