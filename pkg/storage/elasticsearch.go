// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	elastic "github.com/olivere/elastic/v7"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/logg"

	"github.com/spf13/viper"
)

// ElasticSearch contains an elastic.Client we pass around after init.
type ElasticSearch struct {
	esClient *elastic.Client
}

func (es *ElasticSearch) client() *elastic.Client {
	// Lazy initialisation - don't connect to ElasticSearch until we need to
	if es.esClient == nil {
		es.init()
	}
	return es.esClient
}

func (es *ElasticSearch) init() {
	logg.Debug("Initializing ElasticSearch()")

	// Create a client
	var err error
	var url = viper.GetString("elasticsearch.url")
	var username = viper.GetString("elasticsearch.username")
	var password = viper.GetString("elasticsearch.password")
	logg.Debug("Using ElasticSearch URL: %s", url)
	logg.Debug("Using ElasticSearch Username: %s", username)

	// Kubernetes LB with Elasticsearch causes challenges with IP being held on connections.
	// We can create our own custom http client, but then connections take awhile to be marked dead.
	// Syntax below...

	// Create our own http client with Transport set to deal with kubernetes lb and cached IP
	// httpClient := &http.Client{
	//	Transport: &http.Transport{
	//		DisableKeepAlives: true, // change to "false" for cached IP
	//	},
	// }
	// Connect to Elasticsearch, no sniffing due to load balancer. Custom client so no caching of IP.
	// es.esClient, err = elastic.NewClient(elastic.SetURL(url), elastic.SetHttpClient(httpClient), elastic.SetSniff(false))

	// However, that is slow to recover from a connection. We can be faster with simple client where we
	// create the connection each time we want it. I expect this will end up slow at scale, and we'll
	// have to revert to the above implementation.

	if username != "" && password != "" {
		es.esClient, err = elastic.NewSimpleClient(elastic.SetURL(url), elastic.SetBasicAuth(username, password))
	} else {
		es.esClient, err = elastic.NewSimpleClient(elastic.SetURL(url))
	}

	if err != nil {
		// TODO - Add instrumentation here for failed elasticsearch connection
		// If issues - https://github.com/olivere/elastic/wiki/Connection-Problems#how-to-figure-out-connection-problems
		panic(err)
	}
}

// Mapping for attributes based on return values to API
// .raw because it's tokenizing the ID in searches, and won't match. .raw is not analyzed, and not tokenized.
// For more on Elasticsearch tokenization
// https://www.elastic.co/guide/en/elasticsearch/reference/current/analysis-tokenizers.html
// The .keyword field is created on string fields with a different schema allowing aggregation and exact match searches.
// We're aggregating in the Attributes call, and doing exact match searches in GetEvents.

// New Schema that changes all pieces to keywords.
var esFieldMapping = map[string]string{
	"time":           "eventTime",
	"action":         "action.keyword",
	"outcome":        "outcome.keyword",
	"request_path":   "requestPath.keyword",
	"observer_id":    "observer.id.keyword",
	"observer_type":  "observer.typeURI.keyword",
	"target_id":      "target.id.keyword",
	"target_type":    "target.typeURI.keyword",
	"initiator_id":   "initiator.id.keyword",
	"initiator_type": "initiator.typeURI.keyword",
	"initiator_name": "initiator.name.keyword",
}

// FilterQuery takes filter requests, and adds their filter to the ElasticSearch Query
// Handle Filter, Negation of Filter !, and or values separated by ,
func FilterQuery(filter, filtername string, query *elastic.BoolQuery) *elastic.BoolQuery {
	switch {
	case strings.HasPrefix(filter, "!"):
		filter = filter[1:]
		query = query.MustNot(elastic.NewTermQuery(filtername, filter))
	default:
		query = query.Filter(elastic.NewTermQuery(filtername, filter))
	}
	return query
}

// GetEvents grabs events for a given tenantID with filtering.
func (es ElasticSearch) GetEvents(filter *EventFilter, tenantID string) ([]*cadf.Event, int, error) {
	index := indexName(tenantID)
	logg.Debug("Looking for events in index %s", index)

	query := elastic.NewBoolQuery()

	if filter.ObserverType != "" {
		// logg.Debug("Filtering on ObserverType %s", filter.ObserverType)
		query = FilterQuery(filter.ObserverType, esFieldMapping["observer_type"], query)
	}
	if filter.TargetType != "" {
		query = FilterQuery(filter.TargetType, esFieldMapping["target_type"], query)
	}
	if filter.TargetID != "" {
		query = FilterQuery(filter.TargetID, esFieldMapping["target_id"], query)
	}
	if filter.InitiatorType != "" {
		query = FilterQuery(filter.InitiatorType, esFieldMapping["initiator_type"], query)
	}
	if filter.InitiatorID != "" {
		query = FilterQuery(filter.InitiatorID, esFieldMapping["initiator_id"], query)
	}
	if filter.InitiatorName != "" {
		query = FilterQuery(filter.InitiatorName, esFieldMapping["initiator_name"], query)
	}
	if filter.Action != "" {
		query = FilterQuery(filter.Action, esFieldMapping["action"], query)
	}
	if filter.Outcome != "" {
		query = FilterQuery(filter.Outcome, esFieldMapping["outcome"], query)
	}
	if filter.RequestPath != "" {
		query = FilterQuery(filter.RequestPath, esFieldMapping["request_path"], query)
	}

	if len(filter.Time) > 0 {
		for key, value := range filter.Time {
			timeField := esFieldMapping["time"]
			switch key {
			case "lt":
				query = query.Filter(elastic.NewRangeQuery(timeField).Lt(value))
			case "lte":
				query = query.Filter(elastic.NewRangeQuery(timeField).Lte(value))
			case "gt":
				query = query.Filter(elastic.NewRangeQuery(timeField).Gt(value))
			case "gte":
				query = query.Filter(elastic.NewRangeQuery(timeField).Gte(value))
			}
		}
	}

	// Check if a search string is provided in EventFilter
	if filter.Search != "" {
		logg.Debug("Search Feature %s", filter.Search)
		// Create a Query String Query for global text search
		queryStringQuery := elastic.NewQueryStringQuery(filter.Search)
		// Combine the Query String Query with the existing Bool Query
		query = query.Must(queryStringQuery)
	}

	esSearch := es.client().Search().
		Index(index).
		Query(query)

	if filter.Sort != nil {
		for _, fieldOrder := range filter.Sort {
			switch fieldOrder.Order {
			case "asc":
				esSearch = esSearch.Sort(esFieldMapping[fieldOrder.Fieldname], true)
			case "desc":
				esSearch = esSearch.Sort(esFieldMapping[fieldOrder.Fieldname], false)
			}
		}
	}

	offset := int(math.Min(float64(filter.Offset), float64(math.MaxInt32)))
	limit := int(math.Min(float64(filter.Limit), float64(math.MaxInt32)))

	esSearch = esSearch.
		Sort(esFieldMapping["time"], false).
		From(offset).Size(limit)

	searchResult, err := esSearch.Do(context.Background()) // execute
	if err != nil {
		return nil, 0, wrapElasticsearchError(err, tenantID, filter.Search)
	}

	logg.Debug("Got %d hits", searchResult.TotalHits())

	// Construct EventDetail array from search results
	var events []*cadf.Event
	for _, hit := range searchResult.Hits.Hits {
		var de cadf.Event
		err := json.Unmarshal(hit.Source, &de)
		if err != nil {
			return nil, 0, err
		}
		events = append(events, &de)
	}
	total := searchResult.TotalHits()

	return events, int(total), nil
}

// GetEvent Returns EventDetail for a single event.
func (es ElasticSearch) GetEvent(eventID, tenantID string) (*cadf.Event, error) {
	index := indexName(tenantID)
	logg.Debug("Looking for event %s in index %s", eventID, index)

	query := elastic.NewTermQuery("id", eventID)
	logg.Debug("Query: %v", query)

	esSearch := es.client().Search().
		Index(index).
		Query(query)

	searchResult, err := esSearch.Do(context.Background())
	if err != nil {
		logg.Debug("Query failed: %s", err.Error())
		return nil, wrapElasticsearchError(err, tenantID, "")
	}
	total := searchResult.TotalHits()
	logg.Debug("Results: %d", total)

	if total > 0 {
		hit := searchResult.Hits.Hits[0]
		var de cadf.Event
		err := json.Unmarshal(hit.Source, &de)
		return &de, err
	}
	return nil, nil
}

// GetAttributes Return all unique attributes available for filtering
// Possible queries, event_type, dns, identity, etc..
func (es ElasticSearch) GetAttributes(filter *AttributeFilter, tenantID string) ([]string, error) {
	index := indexName(tenantID)

	logg.Debug("Looking for unique attributes for %s in index %s", filter.QueryName, index)

	// ObserverType in this case is not the cadf source, but instead the first part of event_type
	var esName string
	if val, ok := esFieldMapping[filter.QueryName]; ok {
		esName = val
	} else {
		esName = filter.QueryName
	}
	logg.Debug("Mapped Queryname: %s --> %s", filter.QueryName, esName)

	limit := int(math.Min(float64(filter.Limit), float64(math.MaxInt32)))
	queryAgg := elastic.NewTermsAggregation().Size(limit).Field(esName)

	esSearch := es.client().Search().Index(index).Size(limit).Aggregation("attributes", queryAgg)
	searchResult, err := esSearch.Do(context.Background())

	if err != nil {
		return nil, wrapElasticsearchError(err, tenantID, "")
	}

	if searchResult.Hits == nil {
		logg.Debug("expected Hits != nil; got: nil")
	}

	agg := searchResult.Aggregations
	if agg == nil {
		logg.Debug("expected Aggregations, got nil")
	}

	termsAggRes, found := agg.Terms("attributes")
	if !found {
		logg.Debug("Term %s not found in Aggregation", esName)
	}
	if termsAggRes == nil {
		logg.Debug("termsAggRes is nil")
		return nil, nil
	}
	logg.Debug("Number of Buckets: %d", len(termsAggRes.Buckets))

	maxDepth := int(math.Min(float64(filter.MaxDepth), float64(math.MaxInt32)))

	var unique []string
	for _, bucket := range termsAggRes.Buckets {
		logg.Debug("key: %s count: %d", bucket.Key, bucket.DocCount)
		attribute := bucket.Key.(string)

		// Hierarchical Depth Handling
		var att string
		if filter.MaxDepth != 0 && strings.Contains(attribute, "/") {
			s := strings.Split(attribute, "/")
			l := len(s)
			for i := 0; i < maxDepth && i < l; i++ {
				if i != 0 {
					att += "/"
				}
				att += s[i]
			}
			attribute = att
		}

		unique = append(unique, attribute)
	}

	unique = RemoveDuplicates(unique)
	return unique, nil
}

// MaxLimit grabs the configured maxlimit for results
func (es ElasticSearch) MaxLimit() uint {
	maxLimit := viper.GetInt("elasticsearch.max_result_window")
	if maxLimit < 0 {
		return 0
	}
	return uint(maxLimit)
}

// indexName Generates the index name for a given TenantId. If no tenantID defaults to audit-*
// Records for audit-* will not be accessible from a given Tenant.
func indexName(tenantID string) string {
	index := "audit-*"
	if tenantID != "" {
		index = fmt.Sprintf("audit-%s*", tenantID)
	}
	return index
}

// wrapElasticsearchError converts Elasticsearch errors into user-friendly StorageError types
func wrapElasticsearchError(err error, tenantID, searchQuery string) error {
	if err == nil {
		return nil
	}

	// Check if it's an Elasticsearch error with details
	if elasticErr, ok := errext.As[*elastic.Error](err); ok {
		// Log the full error details for debugging
		errdetails, _ := json.Marshal(elasticErr.Details) //nolint:errcheck
		logg.Error("Elasticsearch error - Status: %d, Details: %s", elasticErr.Status, errdetails)

		// Map Elasticsearch HTTP status codes to our error types
		switch elasticErr.Status {
		case http.StatusBadRequest:
			// Check error message for specific error types
			errMsg := err.Error()
			if strings.Contains(errMsg, "parse") || strings.Contains(errMsg, "query_shard_exception") {
				return NewQuerySyntaxError(err, searchQuery)
			}
			if strings.Contains(errMsg, "too_many_buckets") {
				return NewResourceExhaustedError(err, "memory")
			}
			return NewQuerySyntaxError(err, "")

		case http.StatusNotFound:
			// Index not found
			if strings.Contains(err.Error(), "index_not_found") {
				return NewIndexNotFoundError(err, tenantID)
			}
			return NewIndexNotFoundError(err, tenantID)

		case http.StatusRequestTimeout, http.StatusGatewayTimeout:
			return NewTimeoutError(err)

		case http.StatusTooManyRequests:
			return NewRateLimitError(err)

		case http.StatusServiceUnavailable, http.StatusBadGateway:
			return NewConnectionError(err)

		default:
			// For other status codes, return internal error
			return NewInternalError(err)
		}
	}

	// Check for connection errors
	if strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "no such host") ||
		strings.Contains(err.Error(), "timeout") {
		return NewConnectionError(err)
	}

	// Log unknown errors
	logg.Error("Unknown Elasticsearch error: %v", err)
	return NewInternalError(err)
}
