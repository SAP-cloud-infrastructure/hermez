<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Dataplane Audit Events

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

- You need the `audit_admin` role in the project you want to enable.
- You need an object storage bucket (Ceph Swift or S3) in your project to receive events. You can create one yourself (recommended — you keep ownership) or let the service create it on first flush.

### Step 1 — Create your object storage bucket

**Via the Elektra dashboard (recommended):**

1. Open the [Elektra](https://dashboard.cloud.sap) dashboard and navigate to your project.
2. Go to **Object Storage** → **Containers** (Swift) or **Object Storage** → **Buckets** (S3/Ceph).
3. Click **Create Container** / **Create Bucket** and give it a name (e.g. `my-audit-events`).
4. Note down the bucket name — you will need it in the next step.

<details>
<summary>Alternative: create via CLI</summary>

```bash
# Ceph Swift
SWIFT_URL=$(openstack catalog show object-store-ceph -f json | python3 -c "
import sys, json
for e in json.load(sys.stdin)['endpoints']:
    if e['interface'] == 'public': print(e['url']); break
")
TOKEN=$(openstack token issue -f value -c id)
BUCKET_NAME=my-audit-events

curl -si -X PUT \
  -H "X-Auth-Token: $TOKEN" \
  "$SWIFT_URL/$BUCKET_NAME"
# Expected: HTTP/1.1 201 Created
```

</details>

### Step 2 — Enable dataplane routing via hermescli

```bash
hermescli dataplane enable \
  --project-id <your-openstack-project-id> \
  --bucket $BUCKET_NAME
```

> **Note:** hermescli dataplane commands require hermescli v0.x or later. If the command is not available, contact your operator to enable the tenant directly.

### Step 3 — Wait for the config cache to expire

Log Router caches tenant configuration for up to 5 minutes. After that window, incoming events start routing to your bucket. You can monitor progress via the Hermez operator dashboard or by checking your bucket after ~10 minutes.

### Step 4 — Verify objects are arriving

**Via the Elektra dashboard:**

1. Navigate to **Object Storage** → **Containers** / **Buckets** in Elektra.
2. Open your bucket and browse to the `events/_Default/` prefix.
3. After 1–2 hours of Ceph RGW activity in your project, hourly folders appear here.

<details>
<summary>Alternative: verify via CLI</summary>

```bash
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

To stop routing events to your bucket:

```bash
hermescli dataplane disable --project-id <your-openstack-project-id>
```

Events that arrived before disabling are not deleted from your bucket. The config cache takes up to 5 minutes to propagate, after which new events stop being routed.

## Known limitations

- **Config propagation delay**: Changes take effect within ~5 minutes due to the config cache TTL.
- **No backfill**: Events generated before you enabled dataplane routing are not retroactively delivered. Only events arriving after the cache expires are routed to your bucket.
- **Late-arrival shards**: Events occasionally arrive after their hour's primary batch has been flushed. These are stored in addendum shards (`A1_0.json`, `A2_0.json`, …) with their own `manifest_A1.json` and `digest_A1.json` files alongside the primary shard.
