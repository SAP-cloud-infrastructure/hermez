<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Configuration Guide

Hermes is configured using a TOML config file that is by default located in `etc/hermes/hermes.conf`.
An example configuration file is located in etc/ which can help you get started.

#### Main Hermes config

\[hermes\]
* PolicyFilePath - Location of [OpenStack policy file](https://docs.OpenStack.org/security-guide/identity/policies.html) - policy.json file for which roles are required to access audit events.
Example located in `etc/policy.json`
* storage_driver - Storage backend to use. Options: `elasticsearch` (default), `opensearch`, or `mock`. Both Elasticsearch and OpenSearch are fully supported with identical functionality.

#### Choosing a Storage Backend

Hermes supports two storage backends with identical functionality:

**Backend Selection:**
1. Set `storage_driver` in the `[hermes]` section to choose your backend:
   - `storage_driver = "elasticsearch"` (default) - For Elasticsearch 7.x clusters
   - `storage_driver = "opensearch"` - For OpenSearch 1.x/2.x clusters
   - `storage_driver = "mock"` - For testing without a real backend

2. Configure **only** the backend you selected - you do not need to configure both.

3. **Default behavior**: If `storage_driver` is not specified, Hermes defaults to `elasticsearch` for backward compatibility with existing deployments.

#### Storage Backend Configuration

##### Elasticsearch Configuration

\[elasticsearch\]
* url - URL for Elasticsearch cluster (e.g., `http://localhost:9200`)
* username - (Optional) Username for basic authentication (can also use `HERMES_ES_USERNAME` environment variable)
* password - (Optional) Password for basic authentication (can also use `HERMES_ES_PASSWORD` environment variable)
* max_result_window - (Optional) Maximum number of results that can be returned (default: 20000)

##### OpenSearch Configuration

\[opensearch\]
* url - URL for OpenSearch cluster (e.g., `http://localhost:9200`)
* username - (Optional) Username for basic authentication (can also use `HERMES_OS_USERNAME` environment variable)
* password - (Optional) Password for basic authentication (can also use `HERMES_OS_PASSWORD` environment variable)
* max_result_window - (Optional) Maximum number of results that can be returned (default: 20000)

#### Environment Variables

Both storage backends support environment variables for secure credential management:

**For Elasticsearch:**
- `HERMES_ES_USERNAME` - Username for Elasticsearch authentication
- `HERMES_ES_PASSWORD` - Password for Elasticsearch authentication

**For OpenSearch:**
- `HERMES_OS_USERNAME` - Username for OpenSearch authentication
- `HERMES_OS_PASSWORD` - Password for OpenSearch authentication

**Note**: Helm deployments may need updates to properly inject these environment variables. Until Helm charts are updated, credentials must be provided via the config file.

These environment variables can be set in the deployment environment or in your Kubernetes configuration. Environment variables override values in the config file.

#### Example Configurations

**Option 1: Using Elasticsearch (default)**

```bash
# Set credentials via environment variables (recommended for production)
export HERMES_ES_USERNAME="hermes_user"
export HERMES_ES_PASSWORD="secure_password"
```

```toml
[hermes]
# storage_driver = "elasticsearch"  # Can be omitted - elasticsearch is default
PolicyFilePath = "etc/policy.json"

[elasticsearch]
url = "https://elasticsearch.example.com:9200"
# username and password set via environment variables above
max_result_window = "20000"
```

**Option 2: Using OpenSearch**

```bash
# Set credentials via environment variables (recommended for production)
export HERMES_OS_USERNAME="hermes_user"
export HERMES_OS_PASSWORD="secure_password"
```

```toml
[hermes]
storage_driver = "opensearch"  # Must explicitly select opensearch
PolicyFilePath = "etc/policy.json"

[opensearch]
url = "https://opensearch.example.com:9200"
# username and password set via environment variables above
max_result_window = "20000"
```

**Option 3: Credentials in Config File (development only)**

```toml
[hermes]
storage_driver = "opensearch"
PolicyFilePath = "etc/policy.json"

[opensearch]
url = "https://opensearch.example.com:9200"
username = "hermes_user"
password = "secure_password"  # Not recommended for production
max_result_window = "20000"
```

**Option 4: Using OpenSearch with Helm Deployment (Config File Credentials)**

If your Helm deployment does not inject `HERMES_OS_*` environment variables, specify credentials directly in the config file:

```toml
[hermes]
storage_driver = "opensearch"
PolicyFilePath = "etc/policy.json"

[opensearch]
url = "https://opensearch.example.com:9200"
username = "opensearch_user"
password = "opensearch_password"
max_result_window = "20000"
```

**Note**: Helm chart updates may be required to properly inject `HERMES_OS_USERNAME` and `HERMES_OS_PASSWORD` environment variables into the deployment.


#### Integration for OpenStack Keystone
\[keystone\] 
* auth_url - Location of v3 keystone identity - ex. https://keystone.example.com/v3
* username - OpenStack *service* user to authenticate and authorize clients.
* password 
* user_domain_name 
* project_name
* token_cache_time - In order to improve responsiveness and protect Keystone from too much load, Hermes will
re-check authorizations for users by default every 15 minutes (900 seconds).

