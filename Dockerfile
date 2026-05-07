FROM node:22-bookworm AS web-build

WORKDIR /src/web

COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web ./
RUN npm run build

FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gpt-image-web ./cmd/server

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates gosu tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --home-dir /app --create-home appuser

WORKDIR /app

COPY --from=build /out/gpt-image-web /app/gpt-image-web
COPY --from=web-build /src/web/dist /app/web
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

RUN mkdir -p /app/data /app/data/images /app/data/backups \
    && chown -R appuser:appuser /app \
    && chmod +x /usr/local/bin/docker-entrypoint.sh

ENV CHATGPT2API_ADDR=:3000 \
    CHATGPT2API_DATA_DIR=/app/data \
    CHATGPT2API_DB_PATH=/app/data/app.db \
    CHATGPT2API_IMAGES_DIR=/app/data/images \
    CHATGPT2API_BACKUPS_DIR=/app/data/backups \
    CHATGPT2API_WEB_DIR=/app/web \
    CHATGPT2API_BASE_URL= \
    CHATGPT2API_LOG_LEVEL=info

EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/gpt-image-web", "-healthcheck"]

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["/app/gpt-image-web"]
