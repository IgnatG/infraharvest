// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/medialive"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// MediaLiveLister lists the region's MediaLive channels, inputs and
// input security groups.
type MediaLiveLister struct {
	Service
}

// List lists the channels, the inputs and the input security
// groups, all by ID. A list that fails ends there, and the lister goes on
// with the next. When some lists fail, their errors are returned together
// as a partial error (see harvest.Partial); when every one fails, as the
// service's failure.
func (g *MediaLiveLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *MediaLiveLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := medialive.NewFromConfig(config)
	lists := []struct {
		what string
		list func(context.Context, *medialive.Client) error
	}{
		{"channels", g.listChannels},
		{"inputs", g.listInputs},
		{"input security groups", g.listInputSecurityGroups},
	}
	var parts harvest.Parts
	for _, l := range lists {
		parts.Add(nil, l.what, l.list(g.Context(), client))
	}
	return parts.ErrOutOf(len(lists))
}

// listChannels adds each channel as an aws_medialive_channel.
func (g *MediaLiveLister) listChannels(ctx context.Context, client *medialive.Client) error {
	p := medialive.NewListChannelsPaginator(client, &medialive.ListChannelsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, channel := range page.Channels {
			id := aws.ToString(channel.Id)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_medialive_channel", "aws"))
		}
	}
	return nil
}

// listInputs adds each input as an aws_medialive_input.
func (g *MediaLiveLister) listInputs(ctx context.Context, client *medialive.Client) error {
	p := medialive.NewListInputsPaginator(client, &medialive.ListInputsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, input := range page.Inputs {
			id := aws.ToString(input.Id)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_medialive_input", "aws"))
		}
	}
	return nil
}

// listInputSecurityGroups adds each input security group as an
// aws_medialive_input_security_group.
func (g *MediaLiveLister) listInputSecurityGroups(ctx context.Context, client *medialive.Client) error {
	p := medialive.NewListInputSecurityGroupsPaginator(client, &medialive.ListInputSecurityGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, group := range page.InputSecurityGroups {
			id := aws.ToString(group.Id)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_medialive_input_security_group", "aws"))
		}
	}
	return nil
}
