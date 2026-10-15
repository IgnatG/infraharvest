output "role_assignment_id" {
  description = "ID of the Reader role assignment."
  value       = azurerm_role_assignment.reader.id
}
