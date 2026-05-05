FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gpt-image-web ./cmd/server

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --home-dir /app --create-home appuser

WORKDIR /app

COPY --from=build /out/gpt-image-web /app/gpt-image-web
COPY web /app/web

RUN mkdir -p /app/data && chown -R appuser:appuser /app

USER appuser

ENV CHATGPT2API_ADDR=:3000 \
    CHATGPT2API_DATA_DIR=/app/data \
    CHATGPT2API_DB_PATH=/app/data/app.db \
    CHATGPT2API_IMAGES_DIR=/app/data/images \
    CHATGPT2API_WEB_DIR=/app/web \
    CHATGPT2API_BASE_URL= \
    CHATGPT2API_LOG_LEVEL=info

EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/gpt-image-web", "-healthcheck"]

ENTRYPOINT ["/app/gpt-image-web"]
