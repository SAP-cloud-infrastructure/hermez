<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Configuration Guide

Hermes reads a TOML config file. Pass its path with `-f`:

```sh
hermes -f /etc/hermes/hermes.conf
```

Without `-f`, Hermes looks for `hermes.conf` in the current working directory. If that
default file does not exist, Hermes starts with the built-in defaults listed below. A
file given explicitly with `-f` must exist.

`hermes -version` prints the version and exits.

Examples: [`etc/hermes.conf`](../../etc/hermes.conf) for a real deployment, and
[`etc/hermes-mock.conf`](../../etc/hermes-mock.conf) for local development without any
backend (no Keystone, OpenSearch, Postgres or RabbitMQ needed).

Section and key names are not case-sensitive.

## \[hermes\]

| Key | Default | Description |
| --- | --- | --- |
| `keystone_driver` | `keystone` | `keystone` validates tokens against Keystone. `mock` accepts every request without checking the token; development only. |
| `storage_driver` | `opensearch` | `opensearch` reads events from OpenSearch. `mock` serves a few static events; development only. |
| `routing_store_driver` | `postgres` | Store for the per-project dataplane-config. `postgres` needs the `HERMES_PG_*` variables below. `mock` keeps it in memory and loses it on restart. |
| `PolicyFilePath` | (none) | Path of the [OpenStack policy file](https://docs.openstack.org/security-guide/identity/policies.html). Required when `keystone_driver` is `keystone`; Hermes exits at startup if it is empty. See [`etc/policy.json`](../../etc/policy.json) or the identical [`docs/example-policy.json`](../example-policy.json). |

The policy file must define the rules Hermes checks: `event:list`, `event:show`,
`dataplane_config:manage` and `cluster_viewer` (cloud admins, who may read or manage
other projects through the `project_id` parameter).

## \[API\]

| Key | Default | Description |
| --- | --- | --- |
| `ListenAddress` | `0.0.0.0:8788` | Address the HTTP server listens on. |
| `MaxConcurrentRequests` | `0` | Maximum number of requests served at the same time. More requests get HTTP 503 with `Retry-After: 1`. `0` means no limit; production deployments should set a positive value. |
| `CORSAllowedOrigins` | (empty) | List of origins allowed for browser requests, e.g. `["https://dashboard.example.com"]`. Empty allows all origins. |

### \[API.RateLimit\]

Rate limits are per `X-Auth-Token` (per client address when there is no token). A
limiter is only active when both its rate and its burst are greater than 0. Rejected
requests get HTTP 429 and are counted in `hermes_rate_limit_exceeded_total`.

| Key | Default | Description |
| --- | --- | --- |
| `RequestsPerSecond` | `0` | Rate for `/v1/events`, `/v1/events/{event_id}` and `/v1/attributes/{attribute_name}`. |
| `Burst` | `0` | Burst size for the endpoints above. |
| `DownloadRequestsPerSecond` | `0` | Rate for `/v1/events/download`. |
| `DownloadBurst` | `0` | Burst size for `/v1/events/download`. |
| `EvictionInterval` | `5m` | How often idle limiters are removed. Must be a positive duration when rate limiting is on. |
| `MaxIdlePeriod` | `10m` | A limiter not used for this long is removed. |

The dataplane-config endpoints and `/metrics` are not rate-limited.

## \[opensearch\]

| Key | Default | Description |
| --- | --- | --- |
| `url` | `http://localhost:9200` | URL of the OpenSearch cluster. |
| `username` | (empty) | Basic auth user. The `HERMES_OS_USERNAME` environment variable takes precedence. |
| `password` | (empty) | Basic auth password. The `HERMES_OS_PASSWORD` environment variable takes precedence. Basic auth is only used when both user and password are set. |
| `max_result_window` | `20000` | Largest `offset` + `limit` accepted by `GET /v1/events`, largest `limit` for `GET /v1/attributes`, and page size of `GET /v1/events/download`. Should match `index.max_result_window` of the `hermes` index. |
| `response_header_timeout` | `60` | Seconds to wait for OpenSearch response headers. Values of 0 or less mean 60. |
| `query_timeout` | `60` | Query timeout in seconds sent to OpenSearch with each search. If a search times out, `GET /v1/events` and `GET /v1/attributes` return HTTP 504 instead of partial results. Values of 0 or less mean 60. |

## \[keystone\]

Used when `keystone_driver` is `keystone`. Hermes logs in with a service user to
validate client tokens.

| Key | Description |
| --- | --- |
| `auth_url` | Keystone v3 endpoint, e.g. `https://keystone.example.com/v3` |
| `username` | Service user name |
| `password` | Service user password |
| `user_domain_name` | Domain of the service user |
| `project_name` | Project the service user scopes to |
| `project_domain_name` | Domain of that project |

Validated tokens are cached in memory by the process. There is no setting for the cache
lifetime, and no memcached support.

## Environment variables

| Variable | Default | Description |
| --- | --- | --- |
| `HERMES_DEBUG` | `false` | `true` turns on debug logging. |
| `HERMES_OS_USERNAME` | | OpenSearch user; overrides `opensearch.username`. |
| `HERMES_OS_PASSWORD` | | OpenSearch password; overrides `opensearch.password`. |
| `HERMES_PG_HOSTNAME` | `localhost` | Postgres host for the dataplane-config store. |
| `HERMES_PG_PORT` | `5432` | Postgres port. |
| `HERMES_PG_USERNAME` | `hermes` | Postgres user. |
| `HERMES_PG_PASSWORD` | (empty) | Postgres password. Empty logs a security warning at startup. |
| `HERMES_PG_DBNAME` | `hermes` | Postgres database. |
| `HERMES_PG_CONNECTION_OPTIONS` | (empty) | Extra connection options for the Postgres driver. |
| `HERMES_AUDIT_RABBITMQ_QUEUE_NAME` | (empty) | Queue for Hermes' own audit events (dataplane-config changes). If empty, those events are discarded and a warning is logged. |
| `HERMES_AUDIT_RABBITMQ_HOSTNAME` | `localhost` | RabbitMQ host. Only read when the queue name is set. |
| `HERMES_AUDIT_RABBITMQ_PORT` | `5672` | RabbitMQ port. |
| `HERMES_AUDIT_RABBITMQ_USERNAME` | `guest` | RabbitMQ user. |
| `HERMES_AUDIT_RABBITMQ_PASSWORD` | `guest` | RabbitMQ password. |
| `HERMES_INSECURE` | | `1` turns off TLS certificate checks for Keystone connections, for debugging through mitmproxy. Never set it in production. It does not affect OpenSearch. |

No other config key can be set through the environment.

The Postgres migration grants `SELECT` on the `dataplane_config` table to the role
`log-router`, so that role must exist before Hermes starts with the `postgres` store.

## Example

```toml
[hermes]
PolicyFilePath = "/etc/hermes/policy.json"

[API]
ListenAddress = "0.0.0.0:8788"
MaxConcurrentRequests = 64

[API.RateLimit]
RequestsPerSecond = 10.0
Burst = 20
DownloadRequestsPerSecond = 0.2
DownloadBurst = 2

[opensearch]
url = "https://opensearch.example.com:9200"
# username and password from HERMES_OS_USERNAME / HERMES_OS_PASSWORD
max_result_window = "20000"

[keystone]
auth_url = "https://keystone.example.com/v3"
username = "hermes"
password = "secret"
user_domain_name = "Default"
project_name = "service"
project_domain_name = "Default"
```
