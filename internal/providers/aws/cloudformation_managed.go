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

// stackScanMemory is how long a region's scan is reused (see regionStacks):
// long enough for the regional calls of an import to reuse what its global
// call scanned, short enough for a server that outlives the import to see
// the stacks change.
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
	managed map[string]string
	at      time.Time
}

var (
	// stackScansMu guards stackScans.
	stackScansMu sync.Mutex
	// stackScans are the regions scanned lately (see stackScanMemory).
	stackScans = map[stackScanKey]stackScan{}
)

// cloudFormationManaged returns the physical IDs of the resources the
// CloudFormation stacks of the region config names manage, with the
// stack's name. For the global services, such as IAM, it asks every
// region the account has enabled, stackScanParallel at a time: a stack in
// any of them can create a role. A region whose stacks can't be read, as
// one an SCP denies, is reported, and the other regions are still asked.
// Where two regions' stacks name the same physical ID, the first region,
// in DescribeRegions' order, names the stack.
func (p *Provider) cloudFormationManaged(ctx context.Context, config aws.Config) (map[string]string, error) {
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
	found := make([]map[string]string, len(regions))
	errs := make([]error, len(regions))
	slots := make(chan struct{}, stackScanParallel)
	var wg sync.WaitGroup
	for i, region := range regions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			found[i], errs[i] = regionStacks(ctx, config, region)
		}()
	}
	wg.Wait()
	managed := map[string]string{}
	var failed []error
	for i, region := range regions {
		for id, stack := range found[i] {
			if _, ok := managed[id]; !ok {
				managed[id] = stack
			}
		}
		if errs[i] != nil {
			failed = append(failed, fmt.Errorf("CloudFormation stacks in %s: %w", region, errs[i]))
		}
	}
	return managed, errors.Join(failed...)
}

// regionStacks returns what the stacks of region manage (see
// stackResources), from config's credentials. A region scanned in full
// lately with the same credentials isn't scanned again: an import's
// regional calls reuse what its global call scanned, as the global call
// comes first. What a scan that failed found is returned with its error,
// and not remembered.
func regionStacks(ctx context.Context, config aws.Config, region string) (map[string]string, error) {
	credentials, remember := config.Credentials.(*aws.CredentialsCache)
	remember = remember && testConfig == nil
	key := stackScanKey{credentials: credentials, region: region}
	if remember {
		stackScansMu.Lock()
		scan, ok := stackScans[key]
		stackScansMu.Unlock()
		if ok && time.Since(scan.at) < stackScanMemory {
			return scan.managed, nil
		}
	}
	regional := config.Copy()
	regional.Region = region
	managed := map[string]string{}
	if err := stackResources(ctx, cloudformation.NewFromConfig(regional), managed); err != nil {
		return managed, err
	}
	if remember {
		stackScansMu.Lock()
		stackScans[key] = stackScan{managed: managed, at: time.Now()}
		stackScansMu.Unlock()
	}
	return managed, nil
}

// stackResources adds the physical IDs of the resources of every live
// stack to managed, nested stacks included, but for those the stack has
// deleted, as a stack that rolled back lists them.
func stackResources(ctx context.Context, svc *cloudformation.Client, managed map[string]string) error {
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
						managed[id] = name
					}
				}
			}
		}
	}
	return nil
}
