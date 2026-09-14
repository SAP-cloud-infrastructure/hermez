// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"crypto/tls"
	"net/http"
	"os"

	"github.com/sapcc/go-bits/logg"
)

func init() {
	// I have some trouble getting hermes to connect to our staging OpenStack
	// through mitmproxy (which is very useful for development and debugging) when
	// TLS certificate verification is enabled. Therefore, allow to turn it off
	// with an env variable. (It's very important that this is not the standard
	// "DEBUG" variable. "DEBUG" is meant to be useful for production systems,
	// where you definitely don't want to turn off certificate verification.)
	if os.Getenv("HERMES_INSECURE") == "1" {
		// Emit a loud, unmissable warning. This runs before main() configures
		// debug logging, but logg.Error is never gated, so an operator who has
		// accidentally shipped this flag to production will see it in the logs.
		logg.Error("SECURITY WARNING: HERMES_INSECURE=1 disables TLS certificate verification for all outbound HTTPS (Keystone, OpenSearch). This is for local mitmproxy debugging ONLY and must never be set in production.")
		tlsConf := &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // intentional usage of InsecureSkipVerify
		}
		http.DefaultTransport = &http.Transport{
			TLSClientConfig: tlsConf,
			Proxy:           http.ProxyFromEnvironment,
		}
	}
}
