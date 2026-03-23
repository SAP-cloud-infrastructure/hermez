# Hermes (Hermez) - AI Assistant Guide

## Project Overview

Hermes is an OpenStack audit trail service for SAP Converged Cloud. It stores and serves CADF (Cloud Audit Data Federation) events, enabling tenant-scoped access to audit logs from OpenStack services.

**Stack**: Go 1.26 · OpenSearch · Keystone · gorilla/mux · go-bits · Prometheus

## Quick Reference

```bash
make                     # Build binary to build/hermes
make check               # Run all checks (tests + golangci-lint)
make build/cover.out     # Tests with coverage
make build/cover.html    # HTML coverage report
make static-check        # All static analysis
make license-headers     # Add REUSE-compliant headers
```

## Architecture

```
OpenStack (audit middleware) → RabbitMQ → Logstash → OpenSearch → Hermes API
```

Hermes is the **query layer only** — it reads from OpenSearch. It does not ingest events directly. Logstash handles transformation and enrichment.

### Package Layout

| Package | Purpose |
|---------|---------|
| `main.go` | Entry point, config loading, driver selection |
| `pkg/api/` | HTTP handlers, routing (httpapi.Compose pattern), Prometheus metrics |
| `pkg/hermes/` | Business logic — event filtering, list construction |
| `pkg/storage/` | Storage interface + OpenSearch implementation + mock |
| `pkg/identity/` | Keystone token validation |
| `pkg/policy/` | OpenStack policy.json enforcement |
| `pkg/util/` | Policy loading helpers |
| `pkg/test/` | Test fixtures and HTTP test helpers |

### Key Design Decisions

- **Tenant isolation is document-level**: A single `hermes` index with a `tenant_ids` keyword array field, not per-tenant indices. The sentinel value `"*"` (storage.AllTenants) disables tenant filtering for admin queries.
- **httpapi.Compose pattern**: Each API section (V1API, VersionAPI, MetricsAPI) implements `httpapi.API` with `AddTo(*mux.Router)`. No custom router setup.
- **Driver pattern for testability**: `storage.Storage` interface enables mock/real swap. Same for `gopherpolicy.Validator` (keystone/mock). Configured via TOML `hermes.storage_driver` and `hermes.keystone_driver`.
- **ReturnESJSON**: Custom JSON response helper that un-escapes `\u0026` back to `&` for OpenSearch URL compatibility. Use this instead of `respondwith.JSON`.

## API Endpoints

| Method | Path | Handler | Auth Rule |
|--------|------|---------|-----------|
| GET | `/` | List API versions | None |
| GET | `/v1/` | V1 version info | None |
| GET | `/v1/events` | List events (filtered, paginated) | `event:list` |
| GET | `/v1/events/{event_id}` | Single event detail | `event:show` |
| GET | `/v1/attributes/{attribute_name}` | Unique attribute values | `event:list` |
| GET | `/metrics` | Prometheus metrics | None |

## Code Conventions

### SAP CC / go-bits Usage

- **Logging**: `logg.Info()`, `logg.Error()`, `logg.Debug()` — controlled by `HERMES_DEBUG=true`
- **Auth**: `gopherpolicy.Validator` / `gopherpolicy.Token` for Keystone token validation
- **HTTP composition**: `httpapi.Compose()` to assemble router from API modules
- **Error responses**: `respondwith.ErrorText()` for error propagation to HTTP
- **Type assertions**: `errext.As[T]()` from go-bits instead of manual type assertions
- **Startup**: `must.Succeed()` / `must.Return()` for fatal-on-error initialization
- **Env vars**: `osext.GetenvBool()` for boolean env parsing
- **Graceful shutdown**: `httpext.ContextWithSIGINT()` + `httpext.ListenAndServeContext()`

### Build System

- **go-makefile-maker** generates `Makefile` and `.golangci.yaml` from `Makefile.maker.yaml`
- Do NOT edit `Makefile` or `.golangci.yaml` directly — edit `Makefile.maker.yaml` and regenerate
- CI workflows are also auto-generated via go-makefile-maker

### Linting

- golangci-lint v2 with extensive linter set (see `.golangci.yaml`)
- Key linters: `errorlint`, `gosec`, `modernize`, `gocritic`, `perfsprint`
- Import ordering: stdlib → third-party → `github.com/sapcc/hermes` (enforced by goimports)

### Licensing

- Apache-2.0 with REUSE compliance
- All source files must have SPDX headers: `// SPDX-FileCopyrightText:` and `// SPDX-License-Identifier:`
- Run `make license-headers` before committing new files
- Run `make check-reuse` to verify compliance

## Testing

### Running Tests

```bash
make check               # Full check suite (tests + lint)
make build/cover.out     # Tests only, with coverage
```

### Test Patterns

- **Table-driven tests**: See `pkg/api/api_test.go` for the pattern — struct slice with name, method, path, expected status, fixture file
- **Mock drivers**: `storage.Mock{}` provides static CADF data; `mock.NewValidator()` provides permissive auth
- **Test fixtures**: JSON files in `pkg/api/fixtures/` for expected API responses
- **Test policy**: `pkg/test/policy.json` — permissive policy allowing all operations
- **Prometheus registry reset**: Tests must use `prometheus.NewPedanticRegistry()` to avoid metric registration conflicts between subtests

### Adding Tests

When adding new endpoints or modifying responses:
1. Add fixture JSON to `pkg/api/fixtures/`
2. Add table entry in `api_test.go`
3. If new storage behavior is needed, extend `storage.Mock`

## Configuration

### Config File (TOML)

Default: `hermes.conf` (override with `-f /path/to/config.toml`)

```toml
[hermes]
keystone_driver = "keystone"   # or "mock"
storage_driver = "opensearch"  # or "mock"
PolicyFilePath = "etc/policy.json"

[API]
ListenAddress = "0.0.0.0:8788"

[opensearch]
url = "http://localhost:9200"
max_result_window = "20000"
response_header_timeout = 60   # seconds, for slow clusters

[keystone]
auth_url = "https://keystone.example.com/v3"
username = "hermes_service_user"
password = "secret"
user_domain_name = "Default"
project_name = "service"
```

### Environment Variables

| Variable | Purpose |
|----------|---------|
| `HERMES_DEBUG` | Enable debug logging (`true`/`false`) |
| `HERMES_OS_USERNAME` | OpenSearch username (overrides config) |
| `HERMES_OS_PASSWORD` | OpenSearch password (overrides config) |

## OpenSearch Details

- **Index name**: `hermes` (single consolidated datastream, see `storage.indexName()`)
- **Field mapping**: API names → OpenSearch CADF fields defined in `storage.CADFFieldMapping`
- **Keyword fields**: Most filters use `.keyword` suffix for exact-match (e.g., `action.keyword`, `observer.typeURI.keyword`)
- **Tenant filtering**: `tenant_ids` keyword array field on each document
- **Connection pooling**: Custom HTTP transport with `MaxIdleConnsPerHost: 10` (not the default 2)

## Common Pitfalls

- **Don't edit Makefile or .golangci.yaml** — they are generated. Edit `Makefile.maker.yaml`.
- **Use ReturnESJSON, not respondwith.JSON** — needed for `&` character handling in URLs stored in OpenSearch.
- **Tenant ID validation**: Storage methods reject empty tenant IDs and the literal string `"unavailable"`. The wildcard `"*"` is valid for admin queries only.
- **UUID validation on event_id**: The `GetEventDetails` handler validates that event IDs are valid UUIDs before querying storage.
- **Sort/time parameter parsing**: The API supports comma-separated sort and time range parameters with strict validation. Don't bypass the parsing logic.

## Version

Version is defined as a const in `main.go:24`. Update it there for releases.
