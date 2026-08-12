package spot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// Resolving AWS credentials once, and cheaply.
//
// LoadDefaultConfig succeeds without credentials — it only fails on a malformed
// config file — so every AWS-backed path used to discover their absence by
// making a call and waiting for it to fail. On a machine with no credentials
// that cost the full timeout, per path:
//
//	spotinfo --region all --type '^m5.large$'   10.2s
//	spotinfo --type m5.large --with-score       25.3s, then an error
//
// Neither could have succeeded. The probe below asks the credential chain
// directly, once per process, under a short deadline, and both callers then
// decline to build a provider that has nothing to authenticate with.
//
// Deliberately a probe and not a guess: an EC2 instance role, a container role,
// SSO, a profile and the environment all resolve here, so this reports what the
// SDK would actually have used rather than looking for well-known files.
const (
	// credentialProbeTimeout bounds the one probe. IMDS answers in milliseconds
	// on an instance that has a role; off EC2 the address is unroutable and this
	// is the whole cost of finding that out.
	//
	// Generous on purpose. The deadline covers the whole chain, and several
	// legitimate providers are slow: credential_process shells out to a helper,
	// SSO refreshes a token over the network, an assume-role chain makes its own
	// call. Timing one of those out would report "no credentials" for a caller
	// who has them and silently drop live prices and placement scores — a worse
	// failure than the wait this exists to remove. It costs nothing to be
	// generous here because the probe is lazy: it runs only when an AWS call was
	// going to be made anyway, so it lengthens a path that was already failing
	// and never touches a query answered from the feeds.
	credentialProbeTimeout = 5 * time.Second

	// credentialProbeAttempts keeps the probe to a single try. The retries that
	// serve a real API call only multiply the wait for an answer that will not
	// change: absent credentials stay absent.
	credentialProbeAttempts = 1
)

// errNoAWSCredentials reports a credential chain that resolved to nothing.
var errNoAWSCredentials = errors.New("no AWS credentials found")

// awsConfigWithCredentials returns the shared AWS config, or an error when the
// credential chain is empty. Resolved once: the answer cannot change within one
// invocation, and every caller pays the probe at most once between them.
//
// The negative result is cached too, which is the point — a second path must
// not re-probe a chain that just came back empty. That is correct for a
// one-shot CLI and for the MCP server, which resolves credentials at the same
// moment either way; it would be wrong in a long-lived process where an
// operator can add credentials underneath it.
var awsConfigWithCredentials = sync.OnceValues(func() (aws.Config, error) {
	ctx, cancel := context.WithTimeout(context.Background(), credentialProbeTimeout)
	defer cancel()

	// The probe carries its own retry budget so the short one applies to it
	// alone; the config returned for real calls keeps the adaptive retries.
	probeCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRetryMaxAttempts(credentialProbeAttempts),
	)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load AWS config: %w", err)
	}

	if _, retrieveErr := probeCfg.Credentials.Retrieve(ctx); retrieveErr != nil {
		slog.Debug("no AWS credentials; live prices and placement scores are unavailable",
			slog.Any("error", retrieveErr))

		return aws.Config{}, fmt.Errorf("%w: %w", errNoAWSCredentials, retrieveErr)
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRetryMode(aws.RetryModeAdaptive),
		awsconfig.WithRetryMaxAttempts(maxRetryAttempts),
	)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return cfg, nil
})
