<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Data-Plane Events Toggle

Hermes exposes a per-project switch that controls whether **data-plane
events** for your project (low-level Ceph object-store access logs) are
delivered to a dedicated, immutable compliance bucket. The toggle is
**off by default**: no data-plane events are recorded for your project
unless you opt in.

## What this toggle does

When `enabled = true` for your project, the platform's log-router
component picks up the change and begins shipping your project's
data-plane access events to a compliance bucket assigned to your project.
The compliance bucket is operator-managed and is **not** a bucket you
provision; you cannot read or delete its contents through Hermes.

When `enabled = false` (the default), the log-router does not deliver
data-plane events for your project. Already-delivered events stay in the
bucket: disabling does NOT retroactively remove anything.

The bucket is configured for **WORM (write-once-read-many)** retention
via S3 Object-Lock managed by the compliance-bucket tier. In practical
terms, that means events that have been written cannot be modified or
deleted before their retention period expires. This is by design — the
intent of the toggle is to provide a tamper-evident audit trail for
compliance use cases.

## Activation latency

Toggling the flag does **not** take effect instantly. The log-router
refreshes its configuration on a periodic interval, so changes typically
propagate within a few minutes. Once the log-router picks up the new
state, deliveries (or the absence of deliveries) reflect the new value.

## API

Both endpoints are authenticated via your Keystone token and authorized
via OpenStack policy. The token's project scope must match the URL
project ID; cross-project administration is not supported through this
API.

### Read current state — `GET`

```bash
curl -H "X-Auth-Token: $TOKEN" \
  https://hermes.example.com/v1/projects/$PROJECT_ID/data-plane-events
```

Response:

```json
{"enabled": false}
```

A project that has never been configured returns `{"enabled": false}`.

Required role: `audit_viewer` or `project_admin` on the project.

### Toggle state — `PATCH`

```bash
curl -X PATCH \
  -H "X-Auth-Token: $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled": true}' \
  https://hermes.example.com/v1/projects/$PROJECT_ID/data-plane-events
```

Response:

```json
{"enabled": true}
```

Required role: `project_admin` on the project.

Constraints:

* `Content-Type: application/json` is required (with or without
  `; charset=utf-8`). Other content types return `415`.
* The body is capped at 64 KiB (`413` otherwise).
* Unknown fields are rejected (`400`).
* `enabled` is required and must be a boolean.

Idempotent semantics: a PATCH that does not change the stored value is a
no-op. The response still reflects current state, but no audit event is
emitted and no row is rewritten.

## Verifying a change took effect

Toggle changes are themselves audited as CADF events. To verify your
PATCH was accepted and recorded, query the standard events endpoint:

```bash
curl -H "X-Auth-Token: $TOKEN" \
  "https://hermes.example.com/v1/events?action=enable&target_id=$PROJECT_ID"
```

You should see an event with `action = "enable"` (or `disable`),
`target.typeURI = service/audit/data_plane_events`, and an attachment
named `payload` containing the prior and new values.

To check whether deliveries have actually started after enabling, watch
your compliance bucket. There is no Hermes-side surface for the
log-router's bucket-write activity; that is a platform-operator concern.

## Errors

| Status | Meaning |
|--------|---------|
| `400`  | Invalid `project_id` in URL, malformed JSON, missing or unknown field. |
| `401`  | Missing or invalid Keystone token. |
| `403`  | Your token is scoped to a different project than the URL, or you lack the required role. |
| `405`  | Method not allowed. The `Allow` response header lists supported methods. |
| `413`  | Request body exceeds 64 KiB. |
| `415`  | Content-Type is not `application/json`. |
| `500`  | Internal server error. Response body contains an opaque UUID — quote it when filing a support ticket. |
