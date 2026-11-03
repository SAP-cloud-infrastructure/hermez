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

- You need the **`audit_admin`** role, scoped to the project you want to enable. This satisfies the `dataplane_config:manage` policy (which resolves to the `project_admin` rule, defined as `project_scope and role:audit_admin`). Cloud administrators can manage any project's configuration without this role, except enabling it: that needs a token scoped to the project itself. To grant it (requires cloud admin):

  ```sh
  openstack role add --user <username> --project <project-id> audit_admin
  ```
- The **`objectstore_admin`** role in the same project, in addition to `audit_admin`. `audit_admin` does not include it. Enabling routing creates the destination bucket in your project with your own token, and the object store only accepts that from a token carrying this role. Without it, enabling fails with 403.
- Only where the operator turned on writer role assignment: the Log Router writer user needs the `objectstore_admin` role in your project, so that it can write with a token scoped to your project and your project owns every object. In most domains the operator has already given it the role for every project, and enabling just checks that. If the role is missing, enabling assigns it with your token. That needs a token that may assign roles in the project (role `admin` or `role_admin`). Without it, enabling fails with 403 and names the `openstack role add` command a project admin can run; once the role is there, enabling works with your token too. The writer user keeps the role after you disable routing.
- Free object storage quota in the project. The bucket belongs to your project, so the events count against your quota and billing, not ours. Enabling checks this by writing a tiny object (`.hermes-enable-probe`) into the bucket and deleting it again.

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

Enabling needs a token scoped to the project in the path. A token scoped elsewhere, including a cloud administrator's, is rejected with 409, because the bucket has to be created by the project itself.

Where writer role assignment is on, Hermez first makes sure the Log Router writer user has the `objectstore_admin` role in your project, as described in the prerequisites, and creates nothing if that fails. If it assigned the role and a later step fails, the role stays.

On enable, Hermez uses your token to create the `hermes-audit` container (or the `target_bucket` you gave) in your project if it does not exist, and adds entries to its access lists so that your project can read it and Log Router can read and write it. Log Router needs read access as well, because each hour's digest is chained to the previous one. Running it again is safe. It keeps any other entries already in the access lists, and does not change them at all when the entries are already there. Then it writes a tiny object `.hermes-enable-probe` to check that the project has quota left, and deletes it again. If the bucket was already migrated by the operator but is not yet handed over to your project, the enable succeeds and the config is saved; the bucket stays as it is until the handover. Otherwise nothing is saved unless all of this works:

| Status | Message | What to do |
|---|---|---|
| 401 | your token was rejected by the identity service; get a new token | Your token expired or was revoked. Get a new token and try again. |
| 403 | your token needs the objectstore_admin role in this project | Get the `objectstore_admin` role in the project and use a new token. |
| 403 | your token may not assign roles in this project, so Hermes cannot give the dataplane writer user the objectstore_admin role; ... | Ask a project admin (role `admin` or `role_admin`) to run the `openstack role add` command from the message, or to enable dataplane themselves. Then enable again. |
| 403 | object storage denied the request | The object store refused the request for another reason. Check the bucket's access lists. |
| 409 | enable dataplane with a token scoped to the project | Use a token scoped to the project in the path, not a domain-scoped token or one for another project. |
| 409 | container `<name>` exists but is owned by another project | The bucket was not migrated. Ask the operator to migrate it, or choose another `target_bucket`. |
| 409 | this project cannot create more containers | Delete an unused container, or ask for a higher container limit. |
| 409 | container name is not available | Choose another `target_bucket`. |
| 409 | project has no free object storage quota | Free up space or raise the project's object storage quota. |
| 502 | | The object store failed or could not be reached. Try again. |
| 502 | cannot check the dataplane writer role, cannot determine the identity endpoint, or a message starting with "identity service" | Keystone failed or could not be reached. Try again. |
| 503 | object storage for dataplane is not available in this region | The object store this feature uses does not exist in this region. |
| 503 | dataplane bucket provisioning is not configured on this Hermes instance | Ask the operator. |
| 503 | dataplane writer role assignment is not configured on this Hermes instance | Ask the operator. |

You own the bucket. Do not remove the Log Router entries from its access lists or delete the bucket while routing is enabled.

If you later change `target_bucket`, Hermez sets up the new bucket the same way but does not touch the old one. Remove the Log Router entries (`*:<writer user id>`) from the old bucket's read and write access lists yourself if you no longer want Log Router to have access to it. The writer user is the Hermez service user that Log Router also runs as, unless the operator configured another one.

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

Disabling or deleting the configuration does not touch the bucket. Events that arrived before disabling are not deleted from your bucket. The config cache takes up to 5 minutes to propagate, after which new events stop being routed.

## When events are dropped

Log Router writes to your bucket as a separate user that your bucket's write access list allows. If that stops working, it drops your events for the affected hour instead of retrying forever, and there is no other copy of them anywhere. This happens when you:

- remove the Log Router entry from the container's read or write access list,
- delete the bucket, or
- run out of object storage quota.

Re-enable routing (run the same enable command again) to recreate the bucket and restore the access lists. Events dropped in the meantime are not recovered. Failures on our side (timeouts, 5xx) are retried and the events are kept.

## Known limitations

- **Config propagation delay**: Changes take effect within ~5 minutes due to the config cache TTL.
- **No Object Lock or versioning**: the bucket is a plain container. Tamper evidence comes from the signed `manifest.json` and chained `digest.json` files, not from the storage.
- **No backfill**: Events generated before you enabled dataplane routing are not retroactively delivered. Only events arriving after the cache expires are routed to your bucket.
- **Late-arrival shards**: Events occasionally arrive after their hour's primary batch has been flushed. These are stored in addendum shards (`A1_0.json`, `A2_0.json`, …) with their own `manifest_A1.json` and `digest_A1.json` files alongside the primary shard.
