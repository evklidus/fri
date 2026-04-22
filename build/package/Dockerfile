FROM golang:1.25-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN go build -o /fri-api ./cmd/api

FROM alpine:3.22

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /fri-api /usr/local/bin/fri-api
COPY --from=builder /app/web /app/web

ENV APP_PORT=8080
ENV SOURCE_HTML_PATH=/app/web/source/fri-index.html
ENV WEB_DIR=/app/web/static

EXPOSE 8080

CMD ["fri-api"]

