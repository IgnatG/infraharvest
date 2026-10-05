// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aws

import (
	"fmt"
	"log"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

var AlbAllowEmptyValues = []string{"tags.", "^condition."}

type AlbGenerator struct {
	AWSService
}

func (g *AlbGenerator) loadLB(svc *elasticloadbalancingv2.Client) error {
	p := elasticloadbalancingv2.NewDescribeLoadBalancersPaginator(svc, &elasticloadbalancingv2.DescribeLoadBalancersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, lb := range page.LoadBalancers {
			resourceName := StringValue(lb.LoadBalancerName)
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*lb.LoadBalancerArn,
				resourceName,
				"aws_lb",
				"aws",
				AlbAllowEmptyValues,
			))
			err := g.loadLBListener(svc, lb.LoadBalancerArn)
			if err != nil {
				log.Println(err)
			}
		}
	}
	return nil
}

func (g *AlbGenerator) loadLBListener(svc *elasticloadbalancingv2.Client, loadBalancerArn *string) error {
	p := elasticloadbalancingv2.NewDescribeListenersPaginator(svc, &elasticloadbalancingv2.DescribeListenersInput{LoadBalancerArn: loadBalancerArn}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, ls := range page.Listeners {
			resourceName := *ls.ListenerArn
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				resourceName,
				resourceName,
				"aws_lb_listener",
				"aws",
				AlbAllowEmptyValues,
			))
			err := g.loadLBListenerRule(svc, ls.ListenerArn)
			if err != nil {
				log.Println(err)
			}
			err = g.loadLBListenerCertificate(svc, &ls)
			if err != nil {
				log.Println(err)
			}
		}
	}
	return nil
}

func (g *AlbGenerator) loadLBListenerRule(svc *elasticloadbalancingv2.Client, listenerArn *string) error {
	// The SDK has no paginator for DescribeRules.
	return paginateByMarker(func(marker *string) (*string, error) {
		lsrs, err := svc.DescribeRules(g.Context(), &elasticloadbalancingv2.DescribeRulesInput{
			ListenerArn: listenerArn,
			Marker:      marker,
			PageSize:    aws.Int32(400)},
		)
		if err != nil {
			return nil, err
		}
		for _, lsr := range lsrs.Rules {
			if !aws.ToBool(lsr.IsDefault) {
				resourceName := *lsr.RuleArn
				g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
					resourceName,
					resourceName,
					"aws_lb_listener_rule",
					"aws",
					AlbAllowEmptyValues,
				))
			}
		}
		return lsrs.NextMarker, nil
	})
}

func (g *AlbGenerator) loadLBListenerCertificate(svc *elasticloadbalancingv2.Client, loadBalancer *types.Listener) error {
	lcs, err := svc.DescribeListenerCertificates(g.Context(), &elasticloadbalancingv2.DescribeListenerCertificatesInput{
		ListenerArn: loadBalancer.ListenerArn,
	})
	if err != nil {
		return err
	}
	for _, lc := range lcs.Certificates {
		certificateArn := *lc.CertificateArn
		listenerCertificateID := *loadBalancer.ListenerArn + "_" + certificateArn
		// The default certificate belongs to the listener itself.
		if len(loadBalancer.Certificates) > 0 && certificateArn == aws.ToString(loadBalancer.Certificates[0].CertificateArn) {
			continue
		}
		g.Resources = append(g.Resources, terraformutils.NewResource(
			listenerCertificateID,
			listenerCertificateID,
			"aws_lb_listener_certificate",
			"aws",
			map[string]string{
				"listener_arn":    *loadBalancer.ListenerArn,
				"certificate_arn": certificateArn,
			},
			AlbAllowEmptyValues,
			map[string]interface{}{},
		))
	}
	return err
}

func (g *AlbGenerator) loadLBTargetGroup(svc *elasticloadbalancingv2.Client) error {
	p := elasticloadbalancingv2.NewDescribeTargetGroupsPaginator(svc, &elasticloadbalancingv2.DescribeTargetGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, tg := range page.TargetGroups {
			resourceName := StringValue(tg.TargetGroupName)
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*tg.TargetGroupArn,
				resourceName,
				"aws_lb_target_group",
				"aws",
				AlbAllowEmptyValues,
			))
			err := g.loadTargetGroupTargets(svc, tg.TargetGroupArn)
			if err != nil {
				log.Println(err)
			}
		}
	}
	return nil
}

func (g *AlbGenerator) loadTargetGroupTargets(svc *elasticloadbalancingv2.Client, targetGroupArn *string) error {
	targetHealths, err := svc.DescribeTargetHealth(g.Context(), &elasticloadbalancingv2.DescribeTargetHealthInput{
		TargetGroupArn: targetGroupArn,
	})
	if err != nil {
		return err
	}
	for _, tgh := range targetHealths.TargetHealthDescriptions {
		id := fmt.Sprintf("%s-%s", *targetGroupArn, *tgh.Target.Id)
		g.Resources = append(g.Resources, terraformutils.NewResource(
			id,
			id,
			"aws_lb_target_group_attachment",
			"aws",
			map[string]string{
				"target_id":        *tgh.Target.Id,
				"target_group_arn": *targetGroupArn,
			},
			AlbAllowEmptyValues,
			map[string]interface{}{},
		))
	}
	return nil
}

// Generate TerraformResources from AWS API,
func (g *AlbGenerator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}
	svc := elasticloadbalancingv2.NewFromConfig(config)
	if err := g.loadLB(svc); err != nil {
		return err
	}
	if err := g.loadLBTargetGroup(svc); err != nil {
		return err
	}
	return nil
}

func (g *AlbGenerator) PostConvertHook() error {
	for _, r := range g.Resources {
		if r.InstanceInfo.Type != "aws_lb_listener" {
			continue
		}
		if r.InstanceState.Attributes["default_action.0.order"] == "0" {
			delete(r.Item["default_action"].([]interface{})[0].(map[string]interface{}), "order")
		}
	}

	for i, r := range g.Resources {
		if r.InstanceInfo.Type != "aws_lb_listener_rule" {
			continue
		}
		if r.InstanceState.Attributes["action.0.order"] == "0" {
			delete(r.Item["action"].([]interface{})[0].(map[string]interface{}), "order")
		}
		for _, lb := range g.Resources {
			if lb.InstanceInfo.Type != "aws_lb_listener_certificate" {
				continue
			}
			if r.InstanceState.Attributes["certificate_arn"] == lb.InstanceState.Attributes["arn"] {
				g.Resources[i].Item["certificate_arn"] = "${aws_lb_listener_certificate." + lb.ResourceName + ".arn}"
			}
		}
	}

	for _, r := range g.Resources {
		if r.InstanceInfo.Type != "aws_lb" {
			continue
		}
		if val, ok := r.InstanceState.Attributes["access_logs.0.enabled"]; ok && val == "false" {
			delete(r.Item, "access_logs")
		}
	}
	return nil
}
