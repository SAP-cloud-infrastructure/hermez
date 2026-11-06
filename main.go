// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpext"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/mock"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/osext"
	"github.com/spf13/viper"

	"github.com/sapcc/hermes/pkg/api"
	"github.com/sapcc/hermes/pkg/identity"
	"github.com/sapcc/hermes/pkg/routing"
	"github.com/sapcc/hermes/pkg/storage"
)

const version = "1.2.0"

var configPath *string
var showVersion *bool // Add a flag to check for the version.

func main() {
	logg.ShowDebug = osext.GetenvBool("HERMES_DEBUG")
	parseCmdlineFlags()

	// Check if the version flag is set, and if so, print the version and exit.
	if *showVersion {
		fmt.Println("Hermes version:", version)
		os.Exit(0)
	}

	setDefaultConfig()
	readConfig(configPath)

	if viper.GetString("hermes.keystone_driver") == "keystone" && strings.TrimSpace(viper.GetString("hermes.PolicyFilePath")) == "" {
		logg.Fatal("hermes.PolicyFilePath must be set when using the keystone driver")
	}

	// Create the context here so the auditor's delivery goroutine participates
	// in graceful shutdown alongside the HTTP server.
	ctx := httpext.ContextWithSIGINT(context.Background(), 10*time.Second)
	auditor := configuredAuditor(ctx)

	keystoneDriver, serviceUser, identityClient := configuredKeystoneDriver()
	configureDataplaneWriter(serviceUser)
	storageDriver := configuredStorageDriver()
	routingStore := configuredRoutingStore(ctx)

	must.Succeed(api.Server(ctx, keystoneDriver, storageDriver, routingStore, auditor, identityClient))
}

func parseCmdlineFlags() {
	// Get config file location
	configPath = flag.String("f", "hermes.conf", "specifies the location of the TOML-format configuration file")
	showVersion = flag.Bool("version", false, "prints the version of the application")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
}

func setDefaultConfig() {
	viper.SetDefault("hermes.keystone_driver", "keystone")
	viper.SetDefault("hermes.storage_driver", "opensearch")
	viper.SetDefault("hermes.routing_store_driver", "postgres")
	viper.SetDefault("hermes.dataplane_swift_service_type", "object-store-ceph")
	viper.SetDefault("hermes.dataplane_assign_writer_role", false)
	viper.SetDefault("hermes.dataplane_writer_role", "objectstore_admin")
	viper.SetDefault("API.ListenAddress", "0.0.0.0:8788")
	viper.SetDefault("API.RateLimit.RequestsPerSecond", 0.0)
	viper.SetDefault("API.RateLimit.Burst", 0)
	viper.SetDefault("API.RateLimit.DownloadRequestsPerSecond", 0.0)
	viper.SetDefault("API.RateLimit.DownloadBurst", 0)
	viper.SetDefault("API.RateLimit.EvictionInterval", "5m")
	viper.SetDefault("API.RateLimit.MaxIdlePeriod", "10m")
	viper.SetDefault("opensearch.url", "http://localhost:9200")
	viper.SetDefault("opensearch.max_result_window", "20000")
}

func readConfig(configPath *string) {
	// Enable viper to read Environment Variables
	viper.AutomaticEnv()

	// Bind OpenSearch environment variables
	err := viper.BindEnv("opensearch.username", "HERMES_OS_USERNAME")
	if err != nil {
		logg.Fatal(err.Error())
	}
	err = viper.BindEnv("hermes.dataplane_writer_user_id", "HERMES_DATAPLANE_WRITER_USER_ID")
	if err != nil {
		logg.Fatal(err.Error())
	}
	err = viper.BindEnv("hermes.dataplane_swift_service_type", "HERMES_DATAPLANE_SWIFT_SERVICE_TYPE")
	if err != nil {
		logg.Fatal(err.Error())
	}
	err = viper.BindEnv("hermes.dataplane_assign_writer_role", "HERMES_DATAPLANE_ASSIGN_WRITER_ROLE")
	if err != nil {
		logg.Fatal(err.Error())
	}
	err = viper.BindEnv("hermes.dataplane_writer_role", "HERMES_DATAPLANE_WRITER_ROLE")
	if err != nil {
		logg.Fatal(err.Error())
	}
	err = viper.BindEnv("hermes.dataplane_writer_role_id", "HERMES_DATAPLANE_WRITER_ROLE_ID")
	if err != nil {
		logg.Fatal(err.Error())
	}
	err = viper.BindEnv("opensearch.password", "HERMES_OS_PASSWORD")
	if err != nil {
		logg.Fatal(err.Error())
	}

	// Don't read config file if the default config file isn't there,
	//  as we will just fall back to config defaults in that case
	var shouldReadConfig = true
	if _, err := os.Stat(*configPath); os.IsNotExist(err) {
		shouldReadConfig = *configPath != flag.Lookup("f").DefValue
	}
	// Now we sorted that out, read the config
	logg.Debug("Should read config: %v, config file is %s", shouldReadConfig, *configPath)
	if shouldReadConfig {
		viper.SetConfigFile(*configPath)
		viper.SetConfigType("toml")
		must.Succeed(viper.ReadInConfig())
	}
}

// configureDataplaneWriter fills hermes.dataplane_writer_user_id with the
// service user's ID when the operator did not set it, and logs the result. The
// writer must be the user log-router authenticates as, so the log names the
// service user for comparison.
func configureDataplaneWriter(serviceUser identity.ServiceUser) {
	id, source := identity.ResolveWriterUserID(viper.GetString("hermes.dataplane_writer_user_id"), serviceUser.ID)
	viper.Set("hermes.dataplane_writer_user_id", id)
	if id == "" {
		msg := "no dataplane writer user ID: enabling dataplane will fail with 503 until hermes.dataplane_writer_user_id is set"
		if viper.GetString("hermes.keystone_driver") == "keystone" {
			logg.Error(msg)
		} else {
			logg.Info(msg)
		}
		return
	}
	if serviceUser.ID == "" {
		logg.Info("dataplane writer user ID is %s (from %s); Hermes' own service user could not be read; log-router must authenticate as the writer user", id, source)
		return
	}
	logg.Info("dataplane writer user ID is %s (from %s); Hermes' own service user is %s (ID %s); log-router must authenticate as the writer user", id, source, serviceUser, serviceUser.ID)
}

// configuredKeystoneDriver returns the token validator and, for the keystone
// driver, the user of Hermes' own service token and its identity v3 client
// (zero values otherwise).
func configuredKeystoneDriver() (validator gopherpolicy.Validator, serviceUser identity.ServiceUser, identityClient *gophercloud.ServiceClient) {
	driverName := viper.GetString("hermes.keystone_driver")
	switch driverName {
	case "keystone":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tv, user, err := identity.NewTokenValidator(ctx)
		must.Succeed(err)
		return tv, user, tv.IdentityV3
	case "mock":
		logg.Error("SECURITY WARNING: hermes.keystone_driver is set to \"mock\" — all requests are authorized without real Keystone token validation. This must NEVER be used in production.")
		return mock.NewValidator(mock.NewEnforcer(), nil), identity.ServiceUser{}, nil
	default:
		logg.Fatal("unknown keystone_driver %q", driverName)
		return nil, identity.ServiceUser{}, nil // unreachable
	}
}

var openSearchStorage = storage.OpenSearch{}
var mockStorage = storage.Mock{}

func configuredStorageDriver() storage.Storage {
	driverName := viper.GetString("hermes.storage_driver")
	switch driverName {
	case "opensearch":
		return &openSearchStorage
	case "mock":
		logg.Error("SECURITY WARNING: hermes.storage_driver is set to \"mock\" — events are served from static in-memory fixtures, not real OpenSearch data. This must NEVER be used in production.")
		return mockStorage
	default:
		logg.Fatal("unknown storage_driver %q", driverName)
		return nil // unreachable
	}
}

func configuredRoutingStore(ctx context.Context) routing.Store {
	driverName := viper.GetString("hermes.routing_store_driver")
	switch driverName {
	case "postgres":
		return must.Return(routing.NewPostgres(ctx))
	case "mock":
		return routing.NewMock()
	default:
		logg.Fatal("unknown routing_store_driver %q", driverName)
		return nil // unreachable
	}
}

// configuredAuditor builds the audit event publisher.
// When HERMES_AUDIT_RABBITMQ_QUEUE_NAME is set, events are delivered to RabbitMQ.
// Otherwise a null auditor is used — events are logged at DEBUG level and discarded.
// This allows running Hermes without a RabbitMQ connection in development/test environments.
//
// Required env vars (when queue name is set):
//
//	HERMES_AUDIT_RABBITMQ_QUEUE_NAME  — queue to publish to (production: "notifications.info")
//	HERMES_AUDIT_RABBITMQ_HOSTNAME    — broker host (production: "hermes-rabbitmq-notifications.hermes.svc")
//	HERMES_AUDIT_RABBITMQ_PORT        — broker port (default: 5672)
//	HERMES_AUDIT_RABBITMQ_USERNAME    — AMQP username (from vault: hermes/rabbitmq-user/notifications-default/user)
//	HERMES_AUDIT_RABBITMQ_PASSWORD    — AMQP password (from vault: hermes/rabbitmq-user/notifications-default/password)
func configuredAuditor(ctx context.Context) audittools.Auditor {
	if osext.GetenvOrDefault("HERMES_AUDIT_RABBITMQ_QUEUE_NAME", "") == "" {
		logg.Error("HERMES_AUDIT_RABBITMQ_QUEUE_NAME is not set — audit events will be discarded (null auditor)")
		return audittools.NewNullAuditor()
	}
	return must.Return(audittools.NewAuditor(ctx, audittools.AuditorOpts{
		EnvPrefix: "HERMES_AUDIT_RABBITMQ",
		Observer: audittools.Observer{
			TypeURI: "service/hermes",
			Name:    "hermes",
			ID:      audittools.GenerateUUID(),
		},
	}))
}
