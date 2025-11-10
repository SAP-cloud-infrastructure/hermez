<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

* Replacement of Logstash for transferring events from RabbitMQ to
    * If we wish to configure the service via API, it would be significantly easier to have an API against which to edit the configuration. Logstash doesn't enable this, using config files. Those config files can be live changed while the service runs, but this isn't ideal. In later versions of OpenSearch/Logstash there is an API for configuration changes, but it doesn't enable the granular changes we require.
    * We want to send all events to OpenStack Swift, as well as OpenSearch. Logstash can possibly use this with the s3 api, however if we want to do anything like putting events into ProjectID buckets, we're going to be unable to do that.
    * If we want to have a configuration of the service for users, we're going to need an api into the ETL of events.

* Requirements  
    * Different project ids have to provide change auditing for different times in the past.
    * Data should be available for customers to Swift.
    * There is no need to make this data easily searchable.

* All Proposals
    * Offer a configuration option via API and UI on how long the audit log should be archived.
    * Need storage/state for Configuration Options. Database, or Key/Value store.

* Proposal 1 - Take Data from OpenSearch and put it into swift.
    * Implement a swift upload of "chunked" audit messages by a key into a swift container in the customer project.
        * Chunked by day index makes sense. 
        * Export to CSV or Export to JSON
        * Customer Project Swift container requires 
            * access to upload into customer swift container
            * ??? Is it always to the same region container, or can it be different regions?
            * configuration of name of swift container to upload
                * Upload process
                * Amazon uploads every 15 minutes.
                * Amazon has option for uploading data to all regions.
            * configuration of max data to hold 
                * Delete process to remove data from swift container after timeframe
                    * Amazon uses an S3 specific lifecycle manager for data, maybe we don't own the delete process, and that should be owned by swift.
            * Amazon stores data as JSON in buckets, makes sense for us as well.
            * Provides a Java Client library for using the JSON in buckets. 
                * https://github.com/aws/aws-cloudtrail-processing-library


* Proposal 2 - Transfer Data into swift at the Logstash stage.
    * Implement a custom RabbitMQ to ElasticSearch transfer, that will also include pipelines into Swift based on a configuration.

* we need to prepare the API so it can handle "config of the service" by the
 audit admin. similar to AWS we will get quite some audit messages and it 
 would be good in the long run to have hermes allow submission of the below. 
 not for immediate implementation but for consideration in the design.
 
  * activation of audit per type
  * configuration of "time to keep the audit indexes"


* Actions
    * I think we can safely assume that an export of OpenSearch data into JSON or CSV, is going to be the fastest time to value.
    * Start work on an export from OpenSearch to JSON or CSV, and just put it into files.
        * Once it's in files, work on S3 connector to swift for it. 
        * Place files into S3/swift
        
* Features
    * Export by TimeFrame is a must. 

* Questions
    * What to do if the service is down, and misses a run, if it's done every 15 minutes for example
    * Is this process run inside the API, or is it a different export process?
        * first guess is a different process...
    * Are all files group by day. Does it matter how many files there are individually in a day?
    * What are the key attributes in the filename/dir structure?
    * Do the JSON files contain any metadata, or are they flat JSON of events?




