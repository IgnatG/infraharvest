variable "federated_identity_credentials" {
  type    = map(object({ audience = list(string), issuer = string, subject = string }))
  default = {}
}

variable "location" {
  type = string
}

variable "name" {
  type = string
}

variable "parent_id" {
  type = string
}
