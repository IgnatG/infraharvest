package terraformutils

import (
	"reflect"
	"testing"

	"github.com/hashicorp/terraform/terraform"
)

func TestEmptyFiltersParsing(t *testing.T) {
	service := Service{}
	service.ParseFilters([]string{})

	if !reflect.DeepEqual(service.Filter, []ResourceFilter{}) {
		t.Errorf("failed to parse, got %v", service.Filter)
	}
}

func TestIdFiltersParsing(t *testing.T) {
	service := Service{}
	service.ParseFilters([]string{"aws_vpc=myid"})

	if !reflect.DeepEqual(service.Filter, []ResourceFilter{
		{
			ServiceName:      "aws_vpc",
			FieldPath:        "id",
			AcceptableValues: []string{"myid"},
		}}) {
		t.Errorf("failed to parse, got %v", service.Filter)
	}
}

func TestComplexIdFiltersParsing(t *testing.T) {
	service := Service{}
	service.ParseFilters([]string{"resource=id1:'project:dataset_id'"})

	if !reflect.DeepEqual(service.Filter, []ResourceFilter{
		{
			ServiceName:      "resource",
			FieldPath:        "id",
			AcceptableValues: []string{"id1", "project:dataset_id"},
		}}) {
		t.Errorf("failed to parse, got %v", service.Filter)
	}
}

func TestEdgeIdFiltersParsing(t *testing.T) {
	service := Service{}
	service.ParseFilters([]string{"aws_vpc=:myid"})

	if !reflect.DeepEqual(service.Filter, []ResourceFilter{
		{
			ServiceName:      "aws_vpc",
			FieldPath:        "id",
			AcceptableValues: []string{"myid"},
		}}) {
		t.Errorf("failed to parse, got %v", service.Filter)
	}
}

func TestServiceIdCleanupWithFilter(t *testing.T) {
	service := Service{
		Resources: []Resource{{
			InstanceInfo: &terraform.InstanceInfo{
				Type: "type1",
			},
			InstanceState: &terraform.InstanceState{
				ID: "myid",
			}}, {
			InstanceInfo: &terraform.InstanceInfo{
				Type: "type2",
			},
			InstanceState: &terraform.InstanceState{
				ID: "myid",
			}}},
	}
	service.ParseFilters([]string{"type1=:otherId"})
	service.InitialCleanup()

	if !reflect.DeepEqual(len(service.Resources), 1) {
		t.Errorf("failed to cleanup")
	}
}

// Resources of different types can share an ID, like a role and a group of
// the same name. The cleanup used to drop one of them as a duplicate.
func TestServiceIdCleanupKeepsTypesSharingAnId(t *testing.T) {
	service := Service{
		Resources: []Resource{
			{
				InstanceInfo:  &terraform.InstanceInfo{Type: "aws_iam_role", Id: "admins"},
				InstanceState: &terraform.InstanceState{ID: "admins"},
			},
			{
				InstanceInfo:  &terraform.InstanceInfo{Type: "aws_iam_group", Id: "admins"},
				InstanceState: &terraform.InstanceState{ID: "admins"},
			},
			{
				InstanceInfo:  &terraform.InstanceInfo{Type: "aws_iam_group", Id: "admins"},
				InstanceState: &terraform.InstanceState{ID: "admins"},
			},
		},
	}
	service.ParseFilters([]string{"aws_iam_user=:other"})
	service.InitialCleanup()

	var kept []string
	for _, r := range service.Resources {
		kept = append(kept, r.InstanceInfo.Type)
	}
	if want := []string{"aws_iam_role", "aws_iam_group"}; !reflect.DeepEqual(kept, want) {
		t.Errorf("kept %v, want %v", kept, want)
	}
}

func TestServiceAttributeCleanupWithFilter(t *testing.T) {
	service := Service{
		Resources: []Resource{
			{
				InstanceInfo: &terraform.InstanceInfo{
					Type: "aws_vpc",
				},
				InstanceState: &terraform.InstanceState{
					ID: "vpc1",
				},
				Item: mapI("tags", mapI("Name", "some"))},
			{
				InstanceInfo: &terraform.InstanceInfo{
					Type: "aws_vpc",
				},
				InstanceState: &terraform.InstanceState{
					ID: "vpc2",
				},
				Item: mapI("tags", mapI("Name", "default"))}},
	}
	service.ParseFilters([]string{"Name=tags.Name;Value=default"})
	service.PostRefreshCleanup()

	if !reflect.DeepEqual(len(service.Resources), 1) {
		t.Errorf("failed to cleanup")
	}
}

func TestServiceAttributeNameOnlyCleanupWithFilter(t *testing.T) {
	service := Service{
		Resources: []Resource{
			{
				InstanceInfo: &terraform.InstanceInfo{
					Type: "aws_vpc",
				},
				InstanceState: &terraform.InstanceState{
					ID: "vpc1",
				},
				Item: mapI("tags", mapI("Abc", nil))},
			{
				InstanceInfo: &terraform.InstanceInfo{
					Type: "aws_vpc",
				},
				InstanceState: &terraform.InstanceState{
					ID: "vpc2",
				},
				Item: mapI("tags", mapI("Name", "default"))}},
	}
	service.ParseFilters([]string{"Name=tags.Abc"})
	service.PostRefreshCleanup()

	if !reflect.DeepEqual(len(service.Resources), 1) {
		t.Errorf("failed to cleanup")
	}
}
