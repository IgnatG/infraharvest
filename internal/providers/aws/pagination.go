// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"reflect"
)

// stopOnDuplicateToken is an option for every SDK paginator: it stops the
// paginator when a page returns the token that was just sent, which would
// otherwise be requested forever. Each paginator has its own options type
// with the same StopOnDuplicateToken field, so the field is set by name;
// a type without it is left alone.
func stopOnDuplicateToken[O any](options *O) {
	value := reflect.ValueOf(options).Elem()
	if value.Kind() != reflect.Struct {
		return
	}
	if field := value.FieldByName("StopOnDuplicateToken"); field.IsValid() && field.Kind() == reflect.Bool {
		field.SetBool(true)
	}
}

// paginateByMarker calls listPage with the marker returned by the previous
// page until a page returns no marker. Use it for APIs that page by
// NextMarker but have no SDK paginator, such as the WAF list APIs.
func paginateByMarker(listPage func(marker *string) (nextMarker *string, err error)) error {
	var marker *string
	for {
		next, err := listPage(marker)
		if err != nil {
			return err
		}
		if next == nil || *next == "" {
			return nil
		}
		if marker != nil && *next == *marker {
			return fmt.Errorf("pagination did not advance: marker %q returned twice", *next)
		}
		marker = next
	}
}
