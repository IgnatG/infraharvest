# Read-only permissions

Terraform configurations that grant infraharvest the read-only access it needs: [`aws`](aws), [`azure`](azure) and [`google`](google). For `infraharvest drift`, which also reads the roots' state, [`aws-drift`](aws-drift), [`azure-drift`](azure-drift) and [`google-drift`](google-drift) grant the same read-only access and read access to the state alone. What each grants, and how to apply it, is in [docs/permissions.md](../docs/permissions.md).
