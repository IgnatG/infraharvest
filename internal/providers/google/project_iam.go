// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"fmt"
	"strings"

	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/compute/v1"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// iamService lists the project's IAM members.
const iamService = "iam"

// projectIAMMemberType is the Terraform type of one member's grant of one
// role in a project's IAM policy.
const projectIAMMemberType = "google_project_iam_member"

// conditionalPolicyVersion is the IAM policy version that returns
// conditional role bindings as they are: an earlier version leaves their
// conditions out.
const conditionalPolicyVersion = 3

// IamLister lists the grants of the project's IAM policy, one
// google_project_iam_member for each member of each role binding, the iam
// service. Cloud Asset Inventory's resource search doesn't return IAM
// policies, so cloud_asset can't find them; it lists the project's
// service accounts and custom roles. The policy is the project's, so the
// import of global lists it, and a region's lists nothing. Reading it
// needs resourcemanager.projects.getIamPolicy on the project, which
// roles/viewer and roles/iam.securityReviewer grant.
type IamLister struct {
	harvest.ListerBase
}

// List reads the policy. A failure to read it is an error.
func (g *IamLister) List() error {
	region, _ := g.Settings()["region"].(compute.Region)
	if region.Name != "" {
		return nil
	}
	project, err := projectSetting(g.Settings())
	if err != nil {
		return err
	}
	ctx := g.Context()
	svc, err := cloudresourcemanager.NewService(ctx, clientOptions(ctx)...)
	if err != nil {
		return err
	}
	request := &cloudresourcemanager.GetIamPolicyRequest{
		Options: &cloudresourcemanager.GetPolicyOptions{RequestedPolicyVersion: conditionalPolicyVersion},
	}
	policy, err := svc.Projects.GetIamPolicy(project, request).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("reading the IAM policy of project %s: %w", project, err)
	}
	var resources []harvest.Resource
	for _, binding := range policy.Bindings {
		title := ""
		if binding.Condition != nil {
			title = binding.Condition.Title
		}
		for _, member := range binding.Members {
			resources = append(resources, projectIAMMember(project, binding.Role, member, title, g.Provider()))
		}
	}
	g.Resources = resources
	return nil
}

// projectIAMMember is member's grant of role in project, on the condition
// titled title, if any. It imports by the provider's format, the project,
// the role and the member, and the condition's title, separated by spaces.
func projectIAMMember(project, role, member, title, provider string) harvest.Resource {
	parts := []string{project, role, member}
	if title != "" {
		parts = append(parts, title)
	}
	attributes := map[string]string{
		"project": project,
		"role":    role,
		"member":  member,
	}
	if title != "" {
		attributes["condition_title"] = title
	}
	return harvest.Record(strings.Join(parts, " "), grantName(role, member, title), projectIAMMemberType, provider, attributes)
}

// grantName names member's grant of role, as AWS names a role's policy
// attachment after the role and the policy: the member's short name, the
// role's and the condition's title, if any. The grant of roles/run.invoker
// to serviceAccount:deployer@acme.iam.gserviceaccount.com is
// deployer_run.invoker. Before, the name was the role and the whole
// member. Grants whose names come out the same, such as those of two users
// with the same name in different domains, get the labels' suffixes.
func grantName(role, member, title string) string {
	parts := []string{memberName(member), lastSegment(role)}
	if title != "" {
		parts = append(parts, title)
	}
	return strings.Join(parts, "_")
}

// memberName is an IAM member without its kind, and an email without its
// domain: user:ana@acme.example is ana, a deleted service account
// (deleted:serviceAccount:ci@acme.iam.gserviceaccount.com?uid=1) ci,
// domain:acme.example acme.example, a workload identity principal
// (principal://iam.googleapis.com/.../subject/build) the last segment of
// its URI, and allUsers allUsers.
func memberName(member string) string {
	id := strings.TrimPrefix(member, "deleted:")
	if _, rest, ok := strings.Cut(id, ":"); ok {
		id = rest
	}
	id, _, _ = strings.Cut(id, "?")
	if strings.HasPrefix(id, "//") {
		id = lastSegment(strings.TrimRight(id, "/"))
	}
	if local, _, ok := strings.Cut(id, "@"); ok {
		id = local
	}
	if id == "" {
		return member
	}
	return id
}
