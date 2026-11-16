// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkRedactUIOutput redacts the output of a plan of 20,000
// resources, a few of which quote one of 200 secret values, as a large
// root's plans do on every step.
func BenchmarkRedactUIOutput(b *testing.B) {
	values := make([]string, 200)
	for i := range values {
		values[i] = fmt.Sprintf("s3cr3t-value-%04d-Zq9", i)
	}
	forms := TextForms(values)
	var out strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&out, `{"@level":"info","@message":"aws_s3_bucket.b%d: Preparing import... [id=bucket-%d]","type":"resource_drift","change":{"resource":{"addr":"aws_s3_bucket.b%d","resource_type":"aws_s3_bucket"},"action":"update"}}`+"\n", i, i, i)
		if i%1000 == 0 {
			fmt.Fprintf(&out, `{"@level":"error","type":"diagnostic","diagnostic":{"detail":"expected a name, got \"%s\""}}`+"\n", values[i%len(values)])
		}
	}
	data := []byte(out.String())
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		redactUIOutput(data, forms)
	}
}
