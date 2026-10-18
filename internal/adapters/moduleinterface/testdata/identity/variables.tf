variable "federated_identity_credentials" {
  type    = map(object({ audience = list(string), issuer = string, name = string, subject = string }))
  default = {}
}

variable "location" {
  type = string
}

variable "name" {
  type = string
}

variable "resource_group_name" {
  type = string
}
