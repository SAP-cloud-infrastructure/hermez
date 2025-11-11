// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/mock"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/osext"
	"github.com/spf13/viper"

	"github.com/sapcc/hermes/pkg/api"
	"github.com/sapcc/hermes/pkg/identity"
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
	keystoneDriver := configuredKeystoneDriver()
	storageDriver := configuredStorageDriver()

	must.Succeed(api.Server(keystoneDriver, storageDriver))
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
		return must.Return(identity.NewTokenValidator(context.TODO()))
	case "mock":
		return mock.NewValidator(mock.NewEnforcer(), nil)
	default:
		logg.Error("Couldn't match a keystone driver for configured value \"%s\"", driverName)
		return nil
	}
}

var openSearchStorage = storage.OpenSearch{}
var mockStorage = storage.Mock{}

func configuredStorageDriver() storage.Storage {
	driverName := viper.GetString("hermes.storage_driver")
	switch driverName {
	case "opensearch":
		return openSearchStorage
	case "mock":
		return mockStorage
	default:
		logg.Error("Couldn't match a storage driver for configured value \"%s\"", driverName)
		return nil
	}
}
