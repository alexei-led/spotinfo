package sts

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

func MustAwsConfig(ctx context.Context, region, roleARN, externalID string) aws.Config {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		panic(err)
	}
	if (externalID != "") && (roleARN != "") {
		stsSvc := sts.NewFromConfig(cfg)
		creds := stscreds.NewAssumeRoleProvider(stsSvc, roleARN, func(p *stscreds.AssumeRoleOptions) {
			p.ExternalID = &externalID
		})
		cfg.Credentials = aws.NewCredentialsCache(creds)
	}
	return cfg
}
