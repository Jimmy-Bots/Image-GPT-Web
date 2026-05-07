# Test Layout

- `internal/<package>/*_test.go`: package-level unit tests and white-box tests.
- `internal/upstream/chatgpt/*_test.go`: ChatGPT upstream protocol and transport tests.
- `tests/integration/*_test.go`: HTTP/API integration tests that boot a real `api.Server`.

Integration tests must keep runtime state under `t.TempDir()` through the shared
`newTestServer` helpers. Do not write test databases, images, backups, or web
fixtures into the repository root.
