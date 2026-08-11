package main

import (
	"strings"

	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
)

// cloudFlagUsage documents --cloud, which is declared with no default value on
// purpose: "not set" must stay distinguishable from "set to aws". The documented
// default is applied once, in providerID.
const cloudFlagUsage = "cloud provider: aws|gcp|azure (default: aws)"

// machineFilter resolves the machine-type filter from the nearest context that
// set it. One name, one concept: both commands declare --machine, so this reads
// the same flag wherever it was given.
func machineFilter(ctx *cli.Context) string {
	return lineageString(ctx, flagMachine)
}

// providerRegistry is the slice of the compiled provider registry the CLI
// needs, defined next to its consumer.
type providerRegistry interface {
	Get(id cloud.ProviderID) (cloud.Provider, error)
}

// cloudFlag declares --cloud. Both the root command and recommend carry it so
// it can be given on either side of the subcommand.
func cloudFlag() *cli.StringFlag {
	return &cli.StringFlag{Name: flagCloud, Usage: cloudFlagUsage}
}

// providerID resolves --cloud from the nearest context that set it. An unset
// flag is the documented AWS default; anything else must name a recognised
// provider.
func providerID(ctx *cli.Context) (cloud.ProviderID, error) {
	value := strings.TrimSpace(lineageString(ctx, flagCloud))
	if value == "" {
		return cloud.ProviderAWS, nil
	}

	return cloud.ParseProviderID(value)
}

// requestedOS reports the operating system as a capability question. A value
// outside the neutral vocabulary is not one: the provider owns that error, and
// AWS already reports it with its own wording.
func requestedOS(value string) cloud.OperatingSystem {
	instanceOS, err := cloud.ParseOperatingSystem(value)
	if err != nil {
		return ""
	}

	return instanceOS
}

// requestedArchitecture mirrors requestedOS: an unreviewed architecture string
// is rejected by the recommendation validator, not by the capability gate.
func requestedArchitecture(value string) cloud.Architecture {
	architecture, err := cloud.ParseArchitecture(value)
	if err != nil {
		return ""
	}

	return architecture
}

// The recommend command declares no capability request of its own any more.
// cloud.Recommend derives one from the request — the workload decides whether
// published risk is needed — and checks it before acquisition, so a second
// hand-written copy here could only drift from it.
