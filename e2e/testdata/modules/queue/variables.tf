variable "name" {
  description = "The queue's name."
  type        = string
}

variable "content_based_deduplication" {
  description = "Whether a FIFO queue deduplicates by content."
  type        = bool
  default     = null
}

variable "deduplication_scope" {
  description = "messageGroup or queue, for a FIFO queue."
  type        = string
  default     = null
}

variable "delay_seconds" {
  description = "Seconds new messages are hidden for."
  type        = number
  default     = null
}

variable "fifo_queue" {
  description = "Whether the queue is FIFO."
  type        = bool
  default     = null
}

variable "fifo_throughput_limit" {
  description = "perQueue or perMessageGroupId, for a FIFO queue."
  type        = string
  default     = null
}

variable "kms_data_key_reuse_period_seconds" {
  description = "Seconds a data key is reused for."
  type        = number
  default     = null
}

variable "kms_master_key_id" {
  description = "The KMS key that encrypts messages."
  type        = string
  default     = null
}

variable "max_message_size" {
  description = "Largest message, in bytes."
  type        = number
  default     = null
}

variable "message_retention_seconds" {
  description = "Seconds a message is kept for."
  type        = number
  default     = null
}

variable "policy" {
  description = "The queue's access policy, as JSON."
  type        = string
  default     = null
}

variable "receive_wait_time_seconds" {
  description = "Seconds a receive waits for messages."
  type        = number
  default     = null
}

variable "redrive_allow_policy" {
  description = "Which queues may use this one as a dead-letter queue, as JSON."
  type        = string
  default     = null
}

variable "redrive_policy" {
  description = "The dead-letter queue, as JSON."
  type        = string
  default     = null
}

variable "sqs_managed_sse_enabled" {
  description = "Whether SQS encrypts messages with its own keys."
  type        = bool
  default     = null
}

variable "tags" {
  description = "The queue's tags."
  type        = map(string)
  default     = null
}

variable "visibility_timeout_seconds" {
  description = "Seconds a received message is hidden for."
  type        = number
  default     = null
}
