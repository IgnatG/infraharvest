package terraformutils

import (
	"reflect"
	"testing"
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
			InstanceInfo: &InstanceInfo{
				Type: "type1",
			},
			InstanceState: &InstanceState{
				ID: "myid",
			}}, {
			InstanceInfo: &InstanceInfo{
				Type: "type2",
			},
			InstanceState: &InstanceState{
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
				InstanceInfo:  &InstanceInfo{Type: "aws_iam_role", Id: "admins"},
				InstanceState: &InstanceState{ID: "admins"},
			},
			{
				InstanceInfo:  &InstanceInfo{Type: "aws_iam_group", Id: "admins"},
				InstanceState: &InstanceState{ID: "admins"},
			},
			{
				InstanceInfo:  &InstanceInfo{Type: "aws_iam_group", Id: "admins"},
				InstanceState: &InstanceState{ID: "admins"},
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

// Filters on attributes match the attributes the lister recorded.
func TestServiceAttributeCleanupWithFilter(t *testing.T) {
	service := Service{
		Resources: []Resource{
			NewResource("vpc1", "vpc1", "aws_vpc", "aws", map[string]string{"tags.Name": "some"}),
			NewResource("vpc2", "vpc2", "aws_vpc", "aws", map[string]string{"tags.Name": "default"}),
		},
	}
	service.ParseFilters([]string{"Name=tags.Name;Value=default"})
	FilterCleanup(&service, false)

	if len(service.Resources) != 1 || service.Resources[0].InstanceState.ID != "vpc2" {
		t.Errorf("want vpc2 only, got %v", service.Resources)
	}
}

func TestServiceAttributeNameOnlyCleanupWithFilter(t *testing.T) {
	service := Service{
		Resources: []Resource{
			NewResource("vpc1", "vpc1", "aws_vpc", "aws", map[string]string{"tags.Abc": ""}),
			NewResource("vpc2", "vpc2", "aws_vpc", "aws", map[string]string{"tags.Name": "default"}),
		},
	}
	service.ParseFilters([]string{"Name=tags.Abc"})
	FilterCleanup(&service, false)

	if len(service.Resources) != 1 || service.Resources[0].InstanceState.ID != "vpc1" {
		t.Errorf("want vpc1 only, got %v", service.Resources)
	}
}

func TestNewResource(t *testing.T) {
	r := NewSimpleResource("https://sqs/1/my queue", "my queue", "aws_sqs_queue", "aws")

	if r.ResourceName != "tfer--my-0020-queue" || r.RawName != "my queue" {
		t.Errorf("names: got %q, %q", r.ResourceName, r.RawName)
	}
	if got, want := r.InstanceInfo.ResourceAddress(), "aws_sqs_queue.tfer--my-0020-queue"; got != want {
		t.Errorf("address: got %q, want %q", got, want)
	}
	if r.InstanceState.ID != "https://sqs/1/my queue" || r.InstanceState.Attributes == nil {
		t.Errorf("state: got %+v", r.InstanceState)
	}
}
