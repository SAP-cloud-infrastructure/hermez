<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Hermez v1 API Reference

The URLs indicated in the headers of each section are relative to the endpoint URL advertised in the Keystone catalog 
under the service type resources.

## Request headers

### X-Auth-Token

As with all OpenStack services, this header must contain a Keystone token.

## GET /v1/events

Lists a project’s or domain's audit events. The project or domain comes from the 
scope of the authentication token, but can be overridden by a request parameter.

The list of events can be filtered by the parameters passed in via the URL.

Only basic event data will be listed here (event id, event type, resource id,
resource type, user name). Clients must make a separate call to retrieve the full 
CADF payload data for each individual event.

**Parameters**

| **Name** | **Type** | **Description** |
| --- | --- | --- |
| observer_type | string | Selects all events with source similar to this value. |
| target\_type | string | Selects all events related to this resource type. |
| target\_id | string | Selects all events related to this specific resource instance. |
| initiator\_id | string | Selects all events caused by this initiator (usually an OpenStack user ID) |
| initiator\_type | string | Selects all events caused by this initiator type (user or system) |
| initiator\_name | string | Filters events by Initiator Name |
| action | string | Selects all events representing activities of this type. |
| outcome | string | Selects all events based on the activity result (e.g. failed) |
| search | string | Searches all events based on string (e.g. attachments) |
| time | string | Date filter to select all events with _eventTime_ matching the specified criteria. See Date Filters below for more detail. |
| offset | integer | The starting index within the total list of the events that you would like to retrieve. |
| limit | integer | The maximum number of records to return (up to 100). The default limit is 10. |
| sort | string | Determines the sorted order of the returned list. See Sorting below for more detail. |
| domain\_id | string | Selects all events in this domain (requires special permissions). |
| project\_id | string | Selects all events in this project (requires special permissions). |
| details | boolean | Adds attachment details |

**Scope:**

If `domain_id` is specified, only events for that domain (at domain level, e.g. project creation) will be returned.

If `project_id` is specified, only events for that project will be returned.

If *both* are specified, *no events will be returned*.

If neither is specified, then the scope of the client's X-Auth-Token will be used.

**Negate Filters:**

Filter parameters that are contained in the event can be negated with !

For example, to get a list of events not including failed events
```
GET /v1/events?outcome=!failed
```

**Date Filters:**

The value for the `time` parameter is a comma-separated list of time stamps in ISO 
8601 format. The time stamps can be prefixed with any of these comparison operators:
`gt:` (greater-than), `gte:` (greater-than-or-equal), `lt:` (less-than), `lte:` 
(less-than-or-equal).

For example, to get a list of events from May of 2017:
```
GET /v1/events?time=gte:2017-05-01T00:00:00,lt:2017-06-01T00:00:00
```

**Sorting:**

The value of the sort parameter is a comma-separated list of sort keys. Supported 
sort keys include `time`, `observer_type`, `target_type`, `target_id`, `initiator_type`, `initiator_id`, `outcome` and
 `action`.

Each sort key may also include a direction. Supported directions are `:asc` for 
ascending and `:desc` for descending. The service will use `:asc` for every key 
that does not include a direction.

For example, to sort the list from most recently created to oldest:

```
GET /v1/events?sort=time:desc
```

**Request:**

```
GET /v1/events?offset=1&limit=2&sort=time

Headers:
    Accept: application/json
    X-Auth-Token: {keystone_token}
```

**Response:**

This example shows the audit events for adding a role to a user.

```json
{
  "next": "http://{hermes_host}:8788/v1/events?limit=2&offset=3",
  "previous": "http://{hermes_host}:8788/v1/events?limit=2&offset=0",
  "events": [
    {
      "id": "d3f6695e-8a55-5db1-895c-9f7f0910b7a5",
      "eventTime": "2017-11-01T12:28:58.660965+00:00",
      "action": "create/role_assignment",
      "outcome": "success",
      "initiator": {
        "typeURI": "service/security/account/user",
        "id": "21ff350bc75824262c60adfc58b7fd4a7349120b43a990c2888e6b0b88af6398",
      },
      "target": {
        "typeURI": "service/security/account/user",
        "id": "c4d3626f405b99f395a1c581ed630b2d40be8b9701f95f7b8f5b1e2cf2d72c1b",
      },
      "observer": {
        "typeURI": "service/security",
        "id": "0e8a00bf-e36c-5a51-9418-2d56d59c8887"
      }
    }
  ],
  "total": 2
}
```

**Response Attributes**

| **Name** | **Type** | **Description** |
| --- | --- | --- |
| events | list | Contains a list of events. The attributes in the event objects are the same as for an individual event. |
| total | integer | The total number of events available to the user. |
| next | string | A HATEOAS URL to retrieve the next set of events based on the offset and limit parameters. This attribute is only available when the total number of events is greater than offset and limit parameter combined. |
| previous | string | A HATEOAS URL to retrieve the previous set of events based on the offset and limit parameters. This attribute is only available when the request offset is greater than 0. |

**HTTP Status Codes**

| **Code** | **Description** |
| --- | --- |
| 200 | Successful Request |
| 401 | Invalid/expired X-Auth-Token or the token doesn&#39;t have permissions to this resource |

## Event details

**GET /v1/events/<event_id>**

Returns the full CADF payload for an individual
event, e.g.:

```json
{
  "typeURI": "http://schemas.dmtf.org/cloud/audit/1.0/event",
  "id": "7189ce80-6e73-5ad9-bdc5-dcc47f176378",
  "eventTime": "2017-12-18T18:27:32.352893+00:00",
  "action": "create",
  "eventType": "activity",
  "outcome": "success",
  "requestPath": "/v2.0/ports.json",
  "reason": {
    "reasonCode": "201",
    "reasonType": "HTTP"
  },
  "initiator": {
    "typeURI": "service/security/account/user",
    "id": "ba8304b657fb4568addf7116f41b4a16",
    "name": "neutron",
    "domain": "Default",
    "project_id": "ba8304b657fb4568addf7116f41b4a16",
    "host": {
      "address": "127.0.0.1",
       "agent": "python-neutronclient"
    }
  },
  "target": {
    "typeURI": "network/port",
    "id": "7189ce80-6e73-5ad9-bdc5-dcc47f176378",
    "project_id": "ba8304b657fb4568addf7116f41b4a16"
  },
  "observer": {
    "typeURI": "service/network",
    "name": "neutron",
    "id": "7189ce80-6e73-5ad9-bdc5-dcc47f176378"
  }
}
```

## Attributes

**GET /v1/attributes/<attribute_name>**

Returns the unique values of a given attribute, so that you can e.g. have users select from them. Scoped to the 
OpenStack token.

`GET /v1/attributes/action`

returns

```json 
[
  "update/add/floatingip",
  "update/remove/floatingip"
]
```
**Parameters**

| **Name** | **Type** | **Description** | **Default** |
| --- | --- | --- | --- | 
| max_depth | integer | max. depth / level of detail of hierarchical values | infinity / unlimited |
| limit | integer | limit of values returned | 10000 | 

### Hierarchical Values

To support adjustable levels of detail, _type URIs_ and _actions_ in the CADF taxonomy are organized in hierarchies. Classifiers start with the most general classification followed by a theoretically unlimited number of sub-classifications, separated by slashes `/`. 

Examples:
* `update/add/floatingip`
* `network/firewall/rules`

To limit the amount of classifier values exposed to a client, the level of detail of hierarchical attributes can be limited using the `max_depth` parameter. 

Example:

Given the following list of actual values for the `action` attribute, the `attributes` API will have a different result, depending on the `max_depth` parameter.

List:
* `create`
* `delete`
* `update`
* `update/add/floatingip`
* `update/add/security-group`
* `update/remove/floatingip`
* `update/remove/security-group`
* `start`
* `stop`

| max_depth | values |
|-----------|---------------------------------------|
| 1         | create, delete, update, start, stop  |
| 2         | create, delete, update, update/add, update/remove, start, stop |
| 3         | full list (see above) |

The `max_depth` parameter functions separately from the limit parameter. While limit
will limit the total number of records returned, there may be more than the limit 
of values listed to contain all of the various hierarchies. 