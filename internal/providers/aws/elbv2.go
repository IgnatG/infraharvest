// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"log"
	"strconv"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

// AlbLister lists the service "alb": the Elastic Load Balancing v2 load
// balancers (application, network and gateway), each with its listeners,
// the rules and extra certificates of each listener, and then the target
// groups, each with the targets registered in it.
type AlbLister struct {
	Service
}

// albRulesPageSize is how many listener rules one DescribeRules call asks
// for, the most the API allows.
const albRulesPageSize = 400

// List lists the load balancers with their parts, then the target
// groups with their targets. Failing to list load balancers or target
// groups fails the service. A failure further down, for one load balancer,
// listener or target group, such as a permission the role lacks on it, is
// logged and leaves out only that part, so that the rest is still listed.
func (g *AlbLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := elasticloadbalancingv2.NewFromConfig(config)
	if err := g.listLoadBalancers(client); err != nil {
		return err
	}
	return g.listTargetGroups(client)
}

// listLoadBalancers records every load balancer by ARN, named after its
// name, each followed by its listeners.
func (g *AlbLister) listLoadBalancers(client *elasticloadbalancingv2.Client) error {
	p := elasticloadbalancingv2.NewDescribeLoadBalancersPaginator(client, &elasticloadbalancingv2.DescribeLoadBalancersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, lb := range page.LoadBalancers {
			arn := aws.ToString(lb.LoadBalancerArn)
			g.Resources = append(g.Resources, harvest.RecordBare(arn, aws.ToString(lb.LoadBalancerName), "aws_lb", "aws"))
			g.listListeners(client, arn)
		}
	}
	return nil
}

// listListeners records the listeners of the load balancer lbARN by ARN,
// each followed by its rules and its extra certificates. An error is
// logged; the load balancer stays, without the listeners not yet read.
func (g *AlbLister) listListeners(client *elasticloadbalancingv2.Client, lbARN string) {
	input := &elasticloadbalancingv2.DescribeListenersInput{LoadBalancerArn: aws.String(lbARN)}
	p := elasticloadbalancingv2.NewDescribeListenersPaginator(client, input, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			log.Printf("aws: alb: listeners of %s: %v", lbARN, err)
			return
		}
		for _, listener := range page.Listeners {
			arn := aws.ToString(listener.ListenerArn)
			g.Resources = append(g.Resources, harvest.RecordBare(arn, arn, "aws_lb_listener", "aws"))
			g.listListenerRules(client, arn)
			g.listListenerCertificates(client, listener)
		}
	}
}

// listListenerRules records the rules of the listener listenerARN by ARN,
// but its default rule, which Terraform manages as part of the listener.
// DescribeRules has a marker but pages are walked by hand, with the
// largest page size. An error is logged; the listener stays, with its
// certificates.
func (g *AlbLister) listListenerRules(client *elasticloadbalancingv2.Client, listenerARN string) {
	err := paginateByMarker(func(marker *string) (*string, error) {
		page, err := client.DescribeRules(g.Context(), &elasticloadbalancingv2.DescribeRulesInput{
			ListenerArn: aws.String(listenerARN),
			Marker:      marker,
			PageSize:    aws.Int32(albRulesPageSize),
		})
		if err != nil {
			return nil, err
		}
		for _, rule := range page.Rules {
			if aws.ToBool(rule.IsDefault) {
				continue
			}
			arn := aws.ToString(rule.RuleArn)
			g.Resources = append(g.Resources, harvest.RecordBare(arn, arn, "aws_lb_listener_rule", "aws"))
		}
		return page.NextMarker, nil
	})
	if err != nil {
		log.Printf("aws: alb: rules of listener %s: %v", listenerARN, err)
	}
}

// listListenerCertificates records, across pages, the certificates of
// listener beyond its default one, which Terraform sets on the listener
// itself: the one the answer marks as the default, or else the first
// certificate the listener reports. Each is recorded as
// "<listener ARN>_<certificate ARN>", Terraform's import ID. An error is
// logged and ends the listener's certificates there.
func (g *AlbLister) listListenerCertificates(client *elasticloadbalancingv2.Client, listener types.Listener) {
	listenerARN := aws.ToString(listener.ListenerArn)
	defaultARN := ""
	if len(listener.Certificates) > 0 {
		defaultARN = aws.ToString(listener.Certificates[0].CertificateArn)
	}
	input := &elasticloadbalancingv2.DescribeListenerCertificatesInput{ListenerArn: listener.ListenerArn}
	p := elasticloadbalancingv2.NewDescribeListenerCertificatesPaginator(client, input, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			log.Printf("aws: alb: certificates of listener %s: %v", listenerARN, err)
			return
		}
		for _, certificate := range page.Certificates {
			certificateARN := aws.ToString(certificate.CertificateArn)
			if aws.ToBool(certificate.IsDefault) || (defaultARN != "" && certificateARN == defaultARN) {
				continue
			}
			id := listenerARN + "_" + certificateARN
			g.Resources = append(g.Resources, harvest.Record(id, id, "aws_lb_listener_certificate", "aws", map[string]string{
				"listener_arn":    listenerARN,
				"certificate_arn": certificateARN,
			}))
		}
	}
}

// listTargetGroups records every target group by ARN, named after its
// name, each followed by its registered targets.
func (g *AlbLister) listTargetGroups(client *elasticloadbalancingv2.Client) error {
	p := elasticloadbalancingv2.NewDescribeTargetGroupsPaginator(client, &elasticloadbalancingv2.DescribeTargetGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.TargetGroups {
			arn := aws.ToString(group.TargetGroupArn)
			g.Resources = append(g.Resources, harvest.RecordBare(arn, aws.ToString(group.TargetGroupName), "aws_lb_target_group", "aws"))
			g.listTargetGroupAttachments(client, arn)
		}
	}
	return nil
}

// listTargetGroupAttachments records each target registered in the target
// group groupARN by Terraform's import ID: "<group ARN>,<target ID>", then
// ",<port>" when the target has a port, then ",<zone>" when it names an
// availability zone, after an empty port if it has none. The port and the
// zone are recorded as attributes too. DescribeTargetHealth answers with
// every target at once; an error is logged and leaves the group without
// its targets.
func (g *AlbLister) listTargetGroupAttachments(client *elasticloadbalancingv2.Client, groupARN string) {
	out, err := client.DescribeTargetHealth(g.Context(), &elasticloadbalancingv2.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(groupARN),
	})
	if err != nil {
		log.Printf("aws: alb: targets of target group %s: %v", groupARN, err)
		return
	}
	for _, description := range out.TargetHealthDescriptions {
		target := description.Target
		if target == nil {
			continue
		}
		targetID := aws.ToString(target.Id)
		attributes := map[string]string{
			"target_id":        targetID,
			"target_group_arn": groupARN,
		}
		parts := []string{groupARN, targetID}
		port := ""
		if target.Port != nil {
			port = strconv.Itoa(int(*target.Port))
			attributes["port"] = port
			parts = append(parts, port)
		}
		if zone := aws.ToString(target.AvailabilityZone); zone != "" {
			attributes["availability_zone"] = zone
			if port == "" {
				parts = append(parts, "")
			}
			parts = append(parts, zone)
		}
		id := strings.Join(parts, ",")
		g.Resources = append(g.Resources, harvest.Record(id, id, "aws_lb_target_group_attachment", "aws", attributes))
	}
}
