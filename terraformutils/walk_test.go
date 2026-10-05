package terraformutils

import (
	"reflect"
	"testing"
)

func TestEmptyWalkAndGet(t *testing.T) {
	structure := map[string]interface{}{}
	value := WalkAndGet("attr1", structure)

	if !reflect.DeepEqual(value, []interface{}{}) {
		t.Errorf("failed to get value %v", value)
	}
}

func TestEmptyNestedWalkAndGet(t *testing.T) {
	structure := map[string]map[string]interface{}{}
	value := WalkAndGet("attr1.attr2", structure)

	if !reflect.DeepEqual(value, []interface{}{}) {
		t.Errorf("failed to get value %v", value)
	}
}

func TestSimpleWalkAndGet(t *testing.T) {
	structure := map[string]interface{}{
		"attr1": "value",
	}
	value := WalkAndGet("attr1", structure)

	if !reflect.DeepEqual(value, []interface{}{"value"}) {
		t.Errorf("failed to get value %v", value)
	}
}

func TestSimpleArrayWalkAndGet(t *testing.T) {
	structure := map[string][]interface{}{
		"attr1": {"value"},
	}
	value := WalkAndGet("attr1", structure)

	if !reflect.DeepEqual(value, []interface{}{"value"}) {
		t.Errorf("failed to get value %v", value)
	}
}

func TestNestedWalkAndGet(t *testing.T) {
	structure := map[string]map[string]interface{}{
		"attr1": {
			"attr2": "value",
		},
	}
	value := WalkAndGet("attr1.attr2", structure)

	if !reflect.DeepEqual(value, []interface{}{"value"}) {
		t.Errorf("failed to get value %v", value)
	}
}

func TestNestedWalkWithDotInKeyAndGet(t *testing.T) {
	structure := map[string]map[string]interface{}{
		"attr1": {
			"attr2.attr3": "value",
		},
	}
	value := WalkAndGet("attr1.attr2.attr3", structure)

	if !reflect.DeepEqual(value, []interface{}{"value"}) {
		t.Errorf("failed to get value %v", value)
	}
}

func TestNestedArrayWalkAndGet(t *testing.T) {
	structure := mapI("attr1", []interface{}{
		mapI("attr2", "value1"),
		mapI("attr2", "value2")})
	value := WalkAndGet("attr1.attr2", structure)

	if !reflect.DeepEqual(value, []interface{}{"value1", "value2"}) {
		t.Errorf("failed to get value %v", value)
	}
}

func TestNonExistingWalkAndGet(t *testing.T) {
	structure := map[string]interface{}{
		"attr1": "test",
	}
	value := WalkAndGet("attr1.attr2", structure)

	if !reflect.DeepEqual(value, []interface{}{}) {
		t.Errorf("failed to get value %v", value)
	}
}

func TestEmptyWalkAndCheckField(t *testing.T) {
	structure := map[string]interface{}{}
	value := WalkAndCheckField("attr1", structure)

	if !reflect.DeepEqual(value, false) {
		t.Errorf("failed to get value %v", value)
	}
}

func TestSimpleWalkAndCheckField(t *testing.T) {
	structure := map[string]interface{}{
		"attr1": "value",
	}
	value := WalkAndCheckField("attr1", structure)

	if !reflect.DeepEqual(value, true) {
		t.Errorf("failed to get value %v", value)
	}
}

func mapI(key string, value interface{}) map[string]interface{} {
	return map[string]interface{}{key: value}
}
