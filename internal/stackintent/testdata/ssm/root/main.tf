resource "aws_sqs_queue" "jobs" {
  name = "app-jobs"
}

resource "aws_sns_topic" "events" {
  name = "app-events-old"
}

resource "aws_db_subnet_group" "app" {
  name        = "app-group"
  description = "App subnets"
  subnet_ids  = ["subnet-1", "subnet-2"]
}
