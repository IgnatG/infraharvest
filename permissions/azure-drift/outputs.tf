output "role_assignment_ids" {
  description = "IDs of the role assignments: Reader, unless assign_reader is false, and Storage Blob Data Reader on the state's container."
  value       = concat(azurerm_role_assignment.reader[*].id, [azurerm_role_assignment.state.id])
}
