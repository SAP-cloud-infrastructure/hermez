<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

<!-- Logo and Title -->
<div align="center">
  <img src=".github/assets/hermez.png" alt="Hermez Logo" width="250"/>
  <h1>Hermez</h1>
  
  <p><em>An OpenStack audit trail service</em></p>
  <p>
    <code>Audit Trail</code> &nbsp; <code>OpenStack</code> &nbsp; <code>Golang</code>
  </p>

  
  <!-- Badges -->
  <p>
    <a href="https://github.com/sapcc/hermes/actions/workflows/ci.yaml">
      <img src="https://github.com/sapcc/hermes/actions/workflows/ci.yaml/badge.svg" alt="CI Status"/>
    </a>
    <a href="https://goreportcard.com/report/github.com/sapcc/hermes">
      <img src="https://goreportcard.com/badge/github.com/sapcc/hermes" alt="Go Report Card"/>
    </a>
    <a href="https://godoc.org/github.com/sapcc/hermes">
      <img src="https://godoc.org/github.com/sapcc/hermes?status.svg" alt="GoDoc"/>
    </a>
  </p>
  <br/>
</div>

----

**Hermez** is an audit trail service for OpenStack, originally designed for SAP's internal OpenStack Cloud. 

----

## Features

- 📜 Central repository for OpenStack audit events
- 🔐 Identity v3 authentication & project/domain scoping
- ⚙️ Integration with cloud-based audit APIs
- 📈 Exposes Prometheus metrics
- 🧾 CLI support via [HermezCLI](https://github.com/sapcc/hermescli)

----

# The idea: Audit trail for OpenStack

OpenStack has an audit log through OpenStack Audit Middleware, but no way for customers to view these audit events. Hermez enables 
easy access to audit events on a tenant basis, relying on the ELK stack for storage. Now cloud customers can view their project 
level audit events through an API, or as a module in [Elektra](https://github.com/sapcc/elektra), an OpenStack Dashboard.

## Use Cases

The Audit log can be used by information auditors or cloud based audit APIs to track events for a resource in a domain or project. Support teams can validate when customers communicate problems with cloud services, verify what occurred, and view additional detail about the customer issue.

Hermez enables customer access for audit relevant events that occur from OpenStack in an Open Standards CADF Format.
- [CADF Format](https://www.dmtf.org/sites/default/files/standards/documents/DSP0262_1.0.0.pdf)
- [CADF Standards](http://www.dmtf.org/standards/cadf)


<details>
<summary><strong>Dependencies</strong></summary>

- OpenStack
- [OpenStack Audit Middleware](https://github.com/sapcc/openstack-audit-middleware) - To Generate audit events in a WSGI Pipeline
- RabbitMQ - To queue audit events from OpenStack
- Logstash - To transform and route audit events
- **Elasticsearch** or **OpenSearch** - To store audit events for the API to query (Hermez supports both backends)

</details>

<details>
<summary><strong>Installation</strong></summary>

To install Hermez, you can use the Helm charts available at [SAPCC Helm Charts](https://github.com/sapcc/helm-charts/tree/master/openstack/hermes). These charts provide a simple and efficient way to deploy Hermez in a Kubernetes cluster.

In addition to the Helm charts, you can also use the following related repositories and projects to further customize and integrate Hermez into your OpenStack environment:

Related Repositories:
- [OpenStack Audit Middleware](https://github.com/sapcc/openstack-audit-middleware)
- [Hermez CLI Command Line Client](https://github.com/sapcc/hermescli)
- [Hermez Audit Tools for Creation of Events](https://github.com/sapcc/go-bits/tree/master/audittools)
- [GopherCloud Extension for Hermez Audit](https://github.com/sapcc/gophercloud-sapcc/tree/master/audit/v1)
- [SAPCC Go Api Declarations](https://github.com/sapcc/go-api-declarations/tree/main/cadf)

Related Projects:
- [Keystone Event Notifications](https://docs.openstack.org/keystone/pike/advanced-topics/event_notifications.html)

</details>

<details>
<summary><strong>Supported Services</strong></summary>

- [Keystone Identity Service](https://docs.openstack.org/keystone/latest/)
- [Nova Compute Service](https://docs.openstack.org/nova/latest/)
- [Neutron Network Service](https://docs.openstack.org/neutron/latest/)
- [Designate DNS Service](https://docs.openstack.org/designate/latest/)
- [Cinder Block Storage Service](https://docs.openstack.org/cinder/latest/)
- [Manila Shared Filesystem Service](https://docs.openstack.org/manila/latest/)
- [Glance Image Service](https://docs.openstack.org/glance/latest/)
- [Barbican Key Manager Service](https://docs.openstack.org/Barbican/latest/)
- [Ironic Baremetal Service](https://docs.openstack.org/ironic/latest/)
- [Octavia Load Balancer Service](https://docs.openstack.org/octavia/latest/)
- [Limes Quota/Usage Tracking Service](https://github.com/sapcc/limes)
- [Castellum Vertical Autoscaling Service](https://github.com/sapcc/castellum)
- [Keppel Container Image Registry Service](https://github.com/sapcc/keppel)
- [Archer End Point Service](https://github.com/sapcc/archer)
- Cronus Email Service

</details>
</br>

<h2> Documentation </h2>

## For users

- [Hermez Users Guide](./docs/users/index.md)
- [Hermez API Reference](./docs/users/hermez-v1-reference.md)

## For operators

- [Hermez Operators Guide](./docs/operators/operators-guide.md)

## For Audit Clients submitting events

- [Go Bits AuditTools](https://github.com/sapcc/go-bits/tree/master/audittools)

For detailed usage, refer to the documentation provided in doc.go within the audittools package. This includes examples on how to generate audit events and publish them to a RabbitMQ server.

## Support, Feedback, Contributing

This project is open to feature requests/suggestions, bug reports etc. via [GitHub issues](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/creating-an-issue). Contribution and feedback are encouraged and always welcome. For more information about how to contribute, the project structure, as well as additional contribution information, see our [Contribution Guidelines](https://github.com/SAP-cloud-infrastructure/.github/blob/main/CONTRIBUTING.md).

## Security / Disclosure

If you find any bug that may be a security problem, please follow our instructions at [in our security policy](https://github.com/SAP-cloud-infrastructure/.github/blob/main/SECURITY.md) on how to report it. Please do not create GitHub issues for security-related doubts or problems.

## Code of Conduct

We as members, contributors, and leaders pledge to make participation in our community a harassment-free experience for everyone. By participating in this project, you agree to abide by its [Code of Conduct](https://github.com/SAP-cloud-infrastructure/.github/blob/main/CODE_OF_CONDUCT.md) at all times.

## Licensing

Copyright 2017-2025 SAP SE or an SAP affiliate company and hermez contributors. Please see our [LICENSE](LICENSE) for copyright and license information. Detailed information including third-party components and their licensing/copyright information is available [via the REUSE tool](https://api.reuse.software/info/github.com/sapcc/hermes).