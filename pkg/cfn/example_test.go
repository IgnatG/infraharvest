// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn_test

import (
	"fmt"
	"log"
	"strings"

	"github.com/IgnatG/infraharvest/pkg/cfn"
)

func Example() {
	template := `
Parameters:
  BucketName:
    Type: String
    Description: Name prefix of the logs bucket.
Resources:
  Logs:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: !Sub "${BucketName}-logs"
Outputs:
  LogsArn:
    Value: !GetAtt Logs.Arn
`
	t, err := cfn.Parse(strings.NewReader(template))
	if err != nil {
		log.Fatal(err)
	}
	res, err := cfn.Convert(t, cfn.Options{})
	if err != nil {
		log.Fatal(err)
	}
	for _, name := range res.FileNames() {
		fmt.Printf("%s:\n%s", name, res.Files[name])
	}
	fmt.Println("diagnostics:", len(res.Diagnostics))
	// Output:
	// main.tf:
	// # Logs (AWS::S3::Bucket)
	// resource "aws_s3_bucket" "logs" {
	//   bucket = "${var.bucket_name}-logs"
	// }
	// outputs.tf:
	// output "logs_arn" {
	//   value = aws_s3_bucket.logs.arn
	// }
	// variables.tf:
	// variable "bucket_name" {
	//   type        = string
	//   description = "Name prefix of the logs bucket."
	// }
	// diagnostics: 0
}
