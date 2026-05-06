# gpt-image-web

Go refactor prototype for `chatgpt2api`, focused on concurrency, structured storage, and a real user model.

This version intentionally removes the old register/register-machine module. The first milestone keeps the main API surface and management workflows, then migrates the core ChatGPT Web account, text, model, and image-generation paths behind a small upstream interface.

## Current Scope

- Go HTTP backend using the standard library router.
- SQLite storage with WAL mode, busy timeout, indexes, and bounded connection pool.
- User login/register foundation with signed session tokens.
- Admin user management APIs.
- One generated API key per user, managed from the admin user table.
- Account pool CRUD endpoints.
- React/Vite management UI served from `/`.
- Settings, logs, storage info, and health endpoints.
- Local image archive with list/delete APIs and `/images/*` static serving.
- OpenAI-compatible model, image generation/edit, chat completions, responses, and Anthropic messages routes.
- Streaming output for `/v1/chat/completions`, `/v1/responses`, and `/v1/messages`.
- Legacy `/v1/complete` compatibility for text completion clients.
- Sensitive-word filtering and optional OpenAI-compatible AI review before upstream calls.
- Async image task queue with bounded workers for generation and edit tasks.
- Outbound proxy support through `CHATGPT2API_PROXY_URL`.
- Optional OpenAI account register workflow with `inbucket` mailbox provider.

The ChatGPT Web reverse adapter is isolated in `internal/upstream/chatgpt` and uses a browser-like TLS client for endpoints that are sensitive to standard Go HTTP fingerprints.

## Storage Choice

Default: SQLite.

SQLite is a good fit for single-node self-hosted deployment when configured with WAL mode. Reads can run concurrently, writes are serialized, and operational complexity stays very low. For one process with account pools, logs, users, and task records, it is usually the best first database.

Use PostgreSQL later if you need multi-instance deployment, high sustained write volume, or external analytics. The repository layer is intentionally narrow so that a PostgreSQL store can be added without changing handlers.

## Run

```bash
cp .env.example .env
npm --prefix web install
npm --prefix web run build
go mod tidy
go run ./cmd/server
```

Default address: `:3000`.

Docker:

```bash
docker compose up --build
```

The compose file mounts `./data` into the container and serves the management UI on `http://localhost:3000/`.

Frontend development:

```bash
go run ./cmd/server
npm --prefix web run dev
```

Vite proxies `/api`, `/auth`, `/v1`, and `/images` to the Go server on `127.0.0.1:3000`.

Useful environment variables:

- `VERSION`: repository-root version file, used as the default app version when `CHATGPT2API_VERSION` is not set.
- `CHATGPT2API_AUTH_KEY`: optional legacy admin bearer key for compatibility API calls, not web login.
- `CHATGPT2API_ADMIN_EMAIL`: bootstrap admin email.
- `CHATGPT2API_ADMIN_PASSWORD`: bootstrap admin password.
- `CHATGPT2API_SESSION_SECRET`: stable HMAC secret for login sessions.
- `CHATGPT2API_ALLOW_REGISTRATION`: allow public `/auth/register` for normal users.
- `CHATGPT2API_DATA_DIR`: data directory, default `./data`.
- `CHATGPT2API_DB_PATH`: SQLite DB path, default `./data/app.db`.
- `CHATGPT2API_WEB_DIR`: built management UI directory, default `./web/dist`.
- `CHATGPT2API_IMAGES_DIR`: local image archive directory, default `./data/images`.
- `CHATGPT2API_PROXY_URL`: optional outbound proxy, for example `http://localhost:20122`.
- `CHATGPT2API_BASE_URL`: public base URL used by async image tasks when producing archived image URLs.
- `CHATGPT2API_LOG_LEVEL`: `info` or `debug`. Debug includes extra request and image-generation diagnostics.
- `CHATGPT2API_CORS_ALLOWED_ORIGINS`: comma-separated allowed browser origins. Empty means same-origin only.
- `CHATGPT2API_MAX_REQUEST_BODY_MB`: request body cap, default `80`.
- `CHATGPT2API_LOGIN_RATE_LIMIT_MAX`: login attempts per IP/email window, default `8`.
- `CHATGPT2API_LOGIN_RATE_LIMIT_WINDOW_SECONDS`: login rate-limit window, default `300`.
- `CHATGPT2API_REGISTER_INBUCKET_API_BASE`: inbucket API base URL.
- `CHATGPT2API_REGISTER_INBUCKET_DOMAINS`: comma-separated base domains for generated mailboxes.
- `CHATGPT2API_REGISTER_INBUCKET_RANDOM_SUBDOMAIN`: whether to generate random subdomains, default `true`.
- `CHATGPT2API_REGISTER_PROXY_URL`: optional dedicated register proxy; empty falls back to global proxy.
- `CHATGPT2API_REGISTER_MODE`: `total`, `quota`, or `available`.
- `CHATGPT2API_REGISTER_TOTAL`: target count for `total` mode.
- `CHATGPT2API_REGISTER_THREADS`: concurrent register workers.
- `CHATGPT2API_REGISTER_TARGET_QUOTA`: target quota for `quota` mode.
- `CHATGPT2API_REGISTER_TARGET_AVAILABLE`: target account count for `available` mode.
- `CHATGPT2API_REGISTER_CHECK_INTERVAL_SECONDS`: polling interval for target-based batch mode.

## Main Endpoints

- `POST /auth/login`: login with `email/password`, or validate an existing session token.
- `POST /auth/register`: create the first admin, or a normal user when public registration is enabled.
- `GET /api/me`: current identity.
- `GET|POST|PATCH|DELETE /api/users`: admin user management. Creating a user automatically creates one API key.
- `POST /api/users/{user_id}/api-key/reset`: reset that user's single API key.
- `GET /api/me/api-keys`: inspect the current user's single API key metadata.
- `GET|POST|DELETE /api/accounts`: account pool management.
- `POST /api/accounts/refresh`: refresh account profile, plan, model, and image quota state.
- `GET|POST /api/settings`: system settings.
- `GET /api/register/state`: current register runtime and last result.
- `POST /api/register/config`: save register config.
- `POST /api/register/start`: start batch register.
- `POST /api/register/stop`: stop batch register.
- `POST /api/register/run-once`: run one register attempt immediately.
- `GET /api/storage/info`: storage status.
- `GET /api/images`, `POST /api/images/delete`: local image archive management.
- `GET /v1/models`: OpenAI-compatible model list.
- `POST /v1/images/generations`, `/v1/images/edits`, `/v1/chat/completions`, `/v1/complete`, `/v1/responses`, `/v1/messages`: preserved compatibility surface.

Use `Authorization: Bearer <token>` for protected API endpoints. Browser login uses session tokens from `/auth/login`; OpenAI-compatible API calls can also use a generated user API key or the legacy `CHATGPT2API_AUTH_KEY`.

Open `http://localhost:3000/` for the built-in management UI. It covers account pool operations, users and their API keys, image tasks, settings, logs, and a small compatibility playground. Account APIs accept raw `access_token` on create/refresh for upstream calls, but list/update/delete responses expose only `token_ref` plus a masked display value.

## Migration Plan

1. Add optional PostgreSQL storage if multi-instance deployment becomes necessary.
2. Add encrypted-at-rest account token storage if the deployment target needs stronger local secret protection than SQLite file permissions.
