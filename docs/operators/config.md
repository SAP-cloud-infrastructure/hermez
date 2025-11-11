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
* storage_driver - Storage backend to use. Options: `opensearch` (default), or `mock` for testing.

#### Storage Backend Configuration

\[opensearch\]
* url - URL for OpenSearch cluster (e.g., `http://localhost:9200`)
* username - (Optional) Username for basic authentication (can also use `HERMES_OS_USERNAME` environment variable)
* password - (Optional) Password for basic authentication (can also use `HERMES_OS_PASSWORD` environment variable)
* max_result_window - (Optional) Maximum number of results that can be returned (default: 20000)

#### Environment Variables

OpenSearch supports environment variables for secure credential management:

- `HERMES_OS_USERNAME` - Username for OpenSearch authentication
- `HERMES_OS_PASSWORD` - Password for OpenSearch authentication

**Note**: Helm deployments may need updates to properly inject these environment variables. Until Helm charts are updated, credentials must be provided via the config file.

These environment variables can be set in the deployment environment or in your Kubernetes configuration. Environment variables override values in the config file.

#### Example Configurations

**Option 1: Using OpenSearch with Environment Variables (recommended)**

```bash
# Set credentials via environment variables (recommended for production)
export HERMES_OS_USERNAME="hermes_user"
export HERMES_OS_PASSWORD="secure_password"
```

```toml
[hermes]
storage_driver = "opensearch"  # opensearch is the default
PolicyFilePath = "etc/policy.json"

[opensearch]
url = "https://opensearch.example.com:9200"
# username and password set via environment variables above
max_result_window = "20000"
```

**Option 2: Credentials in Config File (development only)**

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

**Option 3: Using OpenSearch with Helm Deployment (Config File Credentials)**

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

