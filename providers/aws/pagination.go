// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "fmt"

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
