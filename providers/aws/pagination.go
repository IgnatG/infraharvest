// Copyright 2026 The Terraformer Authors.
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
