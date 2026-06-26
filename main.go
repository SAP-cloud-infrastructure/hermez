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

	keystoneDriver := configuredKeystoneDriver()
	storageDriver := configuredStorageDriver()
	routingStore := configuredRoutingStore()

	// Create the context here so the auditor's delivery goroutine participates
	// in graceful shutdown alongside the HTTP server.
	ctx := httpext.ContextWithSIGINT(context.Background(), 10*time.Second)
	auditor := configuredAuditor(ctx)

	must.Succeed(api.Server(ctx, keystoneDriver, storageDriver, routingStore, auditor))
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
	viper.SetDefault("API.ListenAddress", "0.0.0.0:8788")
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

func configuredKeystoneDriver() gopherpolicy.Validator {
	driverName := viper.GetString("hermes.keystone_driver")
	switch driverName {
	case "keystone":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return must.Return(identity.NewTokenValidator(ctx))
	case "mock":
		return mock.NewValidator(mock.NewEnforcer(), nil)
	default:
		logg.Fatal("unknown keystone_driver %q", driverName)
		return nil // unreachable
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
		return mockStorage
	default:
		logg.Fatal("unknown storage_driver %q", driverName)
		return nil // unreachable
	}
}

func configuredRoutingStore() routing.Store {
	driverName := viper.GetString("hermes.routing_store_driver")
	switch driverName {
	case "postgres":
		return must.Return(routing.NewPostgres())
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
		EnvPrefix: "HERMES_AUDIT_RABBITMQ_",
		Observer: audittools.Observer{
			TypeURI: "service/hermes",
			Name:    "hermes",
			ID:      audittools.GenerateUUID(),
		},
	}))
}
