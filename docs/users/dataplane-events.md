<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Dataplane Audit Events

> **⚠️ Limited availability.** Dataplane audit events are currently enabled in a limited set of regions and are being rolled out incrementally. If the steps below do not work in your region, the feature is not yet enabled there — contact your operator before proceeding.

In addition to the management-plane audit events already available through Hermez, you can opt in to **dataplane audit events** — records of operations performed directly against data services such as Ceph object storage (Swift/S3).

## Control-plane vs. dataplane events

| | Control-plane (default) | Dataplane (opt-in) |
|---|---|---|
| **What is recorded** | OpenStack API calls (Nova, Neutron, Keystone, …) | Direct data operations (Ceph RGW reads, writes, deletes, …) |
| **Volume** | Low–medium | Potentially very high |
| **Queried via Hermez API** | Yes | No — delivered to your own object storage bucket |
| **Storage** | Shared OpenSearch cluster | Your object storage bucket (in your own project) |
| **Retention** | Cluster default (~3 months) | As long as objects remain in your bucket |

Control-plane events are always available through the standard [Hermez API](./hermes-v1-reference.md). Dataplane events are routed directly to a bucket that you own — they never touch the shared search cluster.

## How dataplane events are delivered

Once you opt in, the delivery flow is:

```
Ceph RGW operations
      │
      ▼
RabbitMQ (dataplane.audit queue)
      │
      ▼
Log Router (validates, buffers, signs)
      │
      ├─► ccadmin/master bucket (admin copy, always written)
      │
      └─► Your object storage bucket (events/_Default/…)
```

Log Router buffers events in one-hour windows and flushes them as a batch. Each flush produces three files per service per hour:

```
events/_Default/<service>/<region>/<YYYY>/<MM>/<DD>/HH:00_HH:59/
  ├─ S0.json         — event data (one JSON object per line, NDJSON)
  ├─ manifest.json   — SHA256 hash of every data file in this batch
  └─ digest.json     — ed25519-signed digest, hash-linked to the previous hour
```

The digest chain lets you detect any tampering or deletion after the fact. See [Verifying the integrity chain](#verifying-the-integrity-chain) below.

## Enabling dataplane events for your project

### Prerequisites

- You need the **`audit_admin`** role, scoped to the project you want to enable. This satisfies the `dataplane_config:manage` policy (which resolves to the `project_admin` rule, defined as `project_scope and role:audit_admin`). Cloud administrators can manage any project's configuration without this role. To grant it (requires cloud admin):

  ```sh
  openstack role add --user <username> --project <project-id> audit_admin
  ```
- A destination bucket to receive events. **Do not create this bucket yourself** — it is provisioned for you as `hermes-audit` when routing is enabled, with Object Lock and versioning so the audit trail is tamper-evident.

### Step 1 — Enable dataplane routing with hermescli

Use [hermescli](https://github.com/sapcc/hermescli) to manage your project's dataplane configuration. Enable routing with the `dataplane-config set` command. For the full command reference and examples, see the [hermescli Dataplane Config documentation](https://github.com/sapcc/hermescli#dataplane-config).

<details>
<summary>Alternative: enable via the REST API directly</summary>

hermescli is a thin wrapper over a single Hermez endpoint. If you cannot install it, call the endpoint directly:

```bash
curl -si -X PUT \
  -H "X-Auth-Token: $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled": true}' \
  "https://<hermez-host>/v1/projects/<your-openstack-project-id>/dataplane-config"
# Expected: HTTP/1.1 200 OK, returning the saved configuration
```

The path `project_id` must match your token's project scope (unless you are a cloud administrator). Events are delivered to the `hermes-audit` bucket, which is provisioned for you.

</details>

### Step 2 — Wait for the config cache to expire

Log Router caches tenant configuration with a configurable TTL (up to 5 minutes by default; as low as 30 seconds in some regions). After that window, incoming events start routing to your bucket. You can monitor progress via the Hermez operator dashboard or by checking your bucket after ~10 minutes.

### Step 3 — Verify objects are arriving

**Via the Elektra dashboard:**

1. Navigate to **Object Storage** → **Containers** / **Buckets** in Elektra.
2. Open your bucket and browse to the `events/_Default/` prefix.
3. After 1–2 hours of Ceph RGW activity in your project, hourly folders appear here.

<details>
<summary>Alternative: verify via CLI</summary>

```bash
SWIFT_URL=$(openstack catalog show object-store-ceph -f json | python3 -c "
import sys, json
for e in json.load(sys.stdin)['endpoints']:
    if e['interface'] == 'public': print(e['url']); break
")
TOKEN=$(openstack token issue -f value -c id)
BUCKET_NAME=hermes-audit

curl -sf \
  -H "X-Auth-Token: $TOKEN" \
  "$SWIFT_URL/$BUCKET_NAME?prefix=events/_Default/&format=json" \
  | python3 -c "
import sys, json
for x in json.load(sys.stdin):
    print(x['name'], x['bytes'], 'bytes')
"
```

</details>

If there is no Ceph RGW activity in your project, no dataplane events are generated and the bucket stays empty.

## Event format

Dataplane events follow the [DMTF CADF specification](https://www.dmtf.org/standards/cadf), the same format used by control-plane events.

### Required fields

| Field | Type | Description |
|-------|------|-------------|
| `typeURI` | string | Always `http://schemas.dmtf.org/cloud/audit/1.0/event` |
| `id` | string | Unique event identifier (UUID) |
| `eventType` | string | `activity`, `monitor`, `control`, or `compliance` |
| `eventTime` | string | RFC 3339 timestamp, e.g. `2026-07-15T09:30:00.000000+00:00` |
| `action` | string | What happened, e.g. `read`, `create`, `delete` |
| `outcome` | string | `success`, `failure`, `pending`, or `unknown` |
| `initiator` | object | Who or what triggered the action (must include `id` and `typeURI`) |
| `target` | object | The resource that was acted on (must include `id` and `typeURI`) |

### Optional fields

| Field | Type | Description |
|-------|------|-------------|
| `observer` | object | The service that observed the action (e.g. `service/storage`) |
| `reason` | object | HTTP status or other reason code |

### Example event (Ceph RGW object read)

```json
{
  "typeURI": "http://schemas.dmtf.org/cloud/audit/1.0/event",
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "eventType": "activity",
  "eventTime": "2026-07-15T09:30:00.000000+00:00",
  "action": "read",
  "outcome": "success",
  "initiator": {
    "typeURI": "service/security/account/user",
    "id": "user-12345",
    "name": "my-user",
    "project_id": "abc123def456"
  },
  "target": {
    "typeURI": "storage/object",
    "id": "my-bucket/my-object.txt"
  },
  "observer": {
    "typeURI": "service/storage",
    "id": "ceph-rgw"
  }
}
```

Events are stored in NDJSON format (one JSON object per line) in the `S0.json` data files.

## Disabling dataplane events

To stop routing events to your bucket, use hermescli to either disable the configuration (`dataplane-config set` with routing turned off) or delete it entirely (`dataplane-config delete`). See the [hermescli Dataplane Config documentation](https://github.com/sapcc/hermescli#dataplane-config) for the exact commands.

Events that arrived before disabling are not deleted from your bucket. The config cache takes up to 5 minutes to propagate, after which new events stop being routed.

## Known limitations

- **Config propagation delay**: Changes take effect within ~5 minutes due to the config cache TTL.
- **No backfill**: Events generated before you enabled dataplane routing are not retroactively delivered. Only events arriving after the cache expires are routed to your bucket.
- **Late-arrival shards**: Events occasionally arrive after their hour's primary batch has been flushed. These are stored in addendum shards (`A1_0.json`, `A2_0.json`, …) with their own `manifest_A1.json` and `digest_A1.json` files alongside the primary shard.
