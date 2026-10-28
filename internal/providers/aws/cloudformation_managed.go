// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// reasonCloudFormation is why the default selection leaves out a resource
// a CloudFormation stack (or the CDK, through one) manages: importing it
// would give it two owners.
func reasonCloudFormation(stack string) string {
	return "managed by CloudFormation stack " + stack
}

// liveStackStatuses are the statuses of every stack that still manages
// resources: all but DELETE_COMPLETE.
var liveStackStatuses = func() []cftypes.StackStatus {
	var live []cftypes.StackStatus
	for _, s := range cftypes.StackStatus("").Values() {
		if s != cftypes.StackStatusDeleteComplete {
			live = append(live, s)
		}
	}
	return live
}()

// stackScanParallel is how many regions cloudFormationManaged scans at
// once. CloudFormation throttles each region on its own.
const stackScanParallel = 4

// stackScanMemory is how long a region's scan is reused (see
// runConfigs.regionStacks): long enough for the regional calls of an
// import to reuse what its global call scanned, short enough for a long
// run to see the stacks change. A run's scans are its own: the next run
// scans again.
const stackScanMemory = 15 * time.Minute

// stackScanKey picks a region's scan: of the credentials of a
// configuration sdkConfig built, which every region of one profile and role
// shares, so of one account, and of the region.
type stackScanKey struct {
	credentials *aws.CredentialsCache
	region      string
}

// stackScan is what stackResources found in a region, and when.
type stackScan struct {
	managed map[string][]stackResource
	at      time.Time
}

// cloudFormationManaged returns the resources the CloudFormation stacks of
// the region config names manage, by physical ID, with the stack's name
// and the resource's CloudFormation type. For the global services, such
// as IAM, it asks every region the account has enabled, stackScanParallel
// at a time: a stack in any of them can create a role. A region whose
// stacks can't be read, as one an SCP denies, is reported, and the other
// regions are still asked.
// Where several stacks name the same physical ID, those of the first
// region, in DescribeRegions' order, come first.
func (p *Provider) cloudFormationManaged(ctx context.Context, config aws.Config) (map[string][]stackResource, error) {
	regions := []string{config.Region}
	if p.region == GlobalPseudoRegion {
		regional := config.Copy()
		regional.Region = "us-east-1"
		out, err := ec2.NewFromConfig(regional).DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
		if err != nil {
			return nil, fmt.Errorf("enabled regions: %w", err)
		}
		regions = regions[:0]
		for _, r := range out.Regions {
			regions = append(regions, aws.ToString(r.RegionName))
		}
	}
	run := p.service(ctx).configs()
	found := make([]map[string][]stackResource, len(regions))
	errs := make([]error, len(regions))
	slots := make(chan struct{}, stackScanParallel)
	var wg sync.WaitGroup
	for i, region := range regions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			found[i], errs[i] = run.regionStacks(ctx, config, region)
		}()
	}
	wg.Wait()
	managed := map[string][]stackResource{}
	var failed []error
	for i, region := range regions {
		for id, resources := range found[i] {
			managed[id] = append(managed[id], resources...)
		}
		if errs[i] != nil {
			failed = append(failed, fmt.Errorf("CloudFormation stacks in %s: %w", region, errs[i]))
		}
	}
	return managed, errors.Join(failed...)
}

// regionStacks returns what the stacks of region manage (see
// stackResources), from config's credentials. A region the run scanned in
// full lately with the same credentials isn't scanned again: an import's
// regional calls reuse what its global call scanned, as the global call
// comes first. What a scan that failed found is returned with its error,
// and not remembered.
func (r *runConfigs) regionStacks(ctx context.Context, config aws.Config, region string) (map[string][]stackResource, error) {
	credentials, remember := config.Credentials.(*aws.CredentialsCache)
	key := stackScanKey{credentials: credentials, region: region}
	if remember {
		r.scansMu.Lock()
		scan, ok := r.stackScans[key]
		r.scansMu.Unlock()
		if ok && time.Since(scan.at) < stackScanMemory {
			return scan.managed, nil
		}
	}
	regional := config.Copy()
	regional.Region = region
	managed := map[string][]stackResource{}
	if err := stackResources(ctx, cloudformation.NewFromConfig(regional), managed); err != nil {
		return managed, err
	}
	if remember {
		r.scansMu.Lock()
		r.stackScans[key] = stackScan{managed: managed, at: time.Now()}
		r.scansMu.Unlock()
	}
	return managed, nil
}

// stackResources adds the resources of every live stack to managed, by
// physical ID, with the stack's name and their CloudFormation type,
// nested stacks included, but for those the stack has deleted, as a stack
// that rolled back lists them.
func stackResources(ctx context.Context, svc *cloudformation.Client, managed map[string][]stackResource) error {
	stacks := cloudformation.NewListStacksPaginator(svc, &cloudformation.ListStacksInput{StackStatusFilter: liveStackStatuses}, stopOnDuplicateToken)
	for stacks.HasMorePages() {
		page, err := stacks.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, s := range page.StackSummaries {
			name := aws.ToString(s.StackName)
			resources := cloudformation.NewListStackResourcesPaginator(svc, &cloudformation.ListStackResourcesInput{StackName: s.StackId}, stopOnDuplicateToken)
			for resources.HasMorePages() {
				page, err := resources.NextPage(ctx)
				if err != nil {
					return fmt.Errorf("stack %s: %w", name, err)
				}
				for _, r := range page.StackResourceSummaries {
					if id := aws.ToString(r.PhysicalResourceId); id != "" && r.ResourceStatus != cftypes.ResourceStatusDeleteComplete {
						managed[id] = append(managed[id], stackResource{stack: name, cloudFormationType: aws.ToString(r.ResourceType)})
					}
				}
			}
		}
	}
	return nil
}
