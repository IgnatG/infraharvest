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
				InstanceInfo:  &InstanceInfo{Type: "aws_iam_role", ID: "admins"},
				InstanceState: &InstanceState{ID: "admins"},
			},
			{
				InstanceInfo:  &InstanceInfo{Type: "aws_iam_group", ID: "admins"},
				InstanceState: &InstanceState{ID: "admins"},
			},
			{
				InstanceInfo:  &InstanceInfo{Type: "aws_iam_group", ID: "admins"},
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

// Filters on attributes match the attributes the lister recorded. Listers
// that support them apply them with ResourceFilter.Filter.
func TestAttributeFilter(t *testing.T) {
	var service Service
	some := NewResource("vpc1", "vpc1", "aws_vpc", "aws", map[string]string{"tags.Name": "some"})
	def := NewResource("vpc2", "vpc2", "aws_vpc", "aws", map[string]string{"tags.Name": "default"})
	abc := NewResource("vpc3", "vpc3", "aws_vpc", "aws", map[string]string{"tags.Abc": ""})

	byValue := service.ParseFilter("Name=tags.Name;Value=default")[0]
	if byValue.Filter(some) || !byValue.Filter(def) {
		t.Errorf("Name=tags.Name;Value=default: some=%v default=%v", byValue.Filter(some), byValue.Filter(def))
	}
	byName := service.ParseFilter("Name=tags.Abc")[0]
	if !byName.Filter(abc) || byName.Filter(def) {
		t.Errorf("Name=tags.Abc: abc=%v default=%v", byName.Filter(abc), byName.Filter(def))
	}
}

// The initial cleanup applies ID filters only: an attribute filter is for
// the lister, and must not drop resources whose lister recorded no such
// attribute.
func TestInitialCleanupLeavesAttributeFiltersToListers(t *testing.T) {
	service := Service{
		Resources: []Resource{
			NewSimpleResource("vpc1", "vpc1", "aws_vpc", "aws"),
			NewSimpleResource("vpc2", "vpc2", "aws_vpc", "aws"),
		},
	}
	service.ParseFilters([]string{"Name=tags.Name;Value=default", "aws_vpc=vpc2"})
	service.InitialCleanup()

	if len(service.Resources) != 1 || service.Resources[0].InstanceState.ID != "vpc2" {
		t.Errorf("want vpc2 only, got %v", service.Resources)
	}
}

func TestNewResource(t *testing.T) {
	r := NewSimpleResource("https://sqs/1/my queue", "my queue", "aws_sqs_queue", "aws")

	if r.ResourceName != "tfer--my-0020-queue" || r.RawName != "my queue" {
		t.Errorf("names: got %q, %q", r.ResourceName, r.RawName)
	}
	if got, want := r.InstanceInfo.ID, "aws_sqs_queue.tfer--my-0020-queue"; got != want || r.InstanceInfo.Type != "aws_sqs_queue" {
		t.Errorf("info: got %+v, want ID %q", r.InstanceInfo, want)
	}
	if r.InstanceState.ID != "https://sqs/1/my queue" || r.InstanceState.Attributes == nil {
		t.Errorf("state: got %+v", r.InstanceState)
	}
}
