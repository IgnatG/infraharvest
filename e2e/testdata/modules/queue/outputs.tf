output "arn" {
  description = "The queue's ARN."
  value       = aws_sqs_queue.this.arn
}

output "url" {
  description = "The queue's URL."
  value       = aws_sqs_queue.this.url
}
