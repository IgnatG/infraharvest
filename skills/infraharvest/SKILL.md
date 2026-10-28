---
name: infraharvest
description: Bring existing AWS, Azure or Google Cloud resources under Terraform or OpenTofu with infraharvest, audit what no state manages, and check or accept drift. Use when the user wants to import, adopt or codify infrastructure that already exists, find unmanaged resources, or see what changed in the cloud since the roots were applied.
---

# infraharvest

infraharvest lists cloud resources, imports the ones a selection file includes, and writes Terraform roots that plan with no changes. It only reads the cloud and writes files: it never applies anything and writes no state. Your job is to run it with the user, never past them.

## Rules

- Never run `terraform apply`, `tofu apply`, `terraform import` or any command that changes state or the cloud. Applying is the user's step.
- Never write a secret value into a file, a prompt or a command. Generated roots take secrets as `sensitive` variables without defaults; the user sets them from their secret store or with `TF_VAR_<name>`.
- Never start an import, a drift run or a drift accept the user hasn't agreed to. Show what it will read or change first.
- Use the infraharvest MCP tools when they are available (`discover`, `import`, `report`, `drift`, `drift_accept`): `import`, `drift` and `drift_accept` ask the user to confirm themselves. Otherwise use the CLI, and ask the user before each of those commands.
- Never edit `selection.yaml` or a drift decisions file after the user has confirmed a run on it: the MCP server then refuses the run.
- Don't pass `--force-unlock`, `--include-owned` or `--allow-partial` unless the user asks for them.

## Workflow

### 1. Discover

Ask which provider, which accounts, subscriptions or projects, which regions and which services. `infraharvest services <provider>` lists the services; `--services='*'` takes them all.

```sh
infraharvest discover aws --profile prod --services=vpc,subnet,sg,s3 --regions=eu-west-2 --pick=false
infraharvest discover azure --services=resource_graph --resource-group=web --pick=false
infraharvest discover google --services='*' --projects=my-project --regions=global,europe-west2 --pick=false
```

`--pick=false` skips the terminal picker. If part of the estate is already under Terraform, add `--managed-state` with that state (state files, a directory, `s3://`, `gs://`, an Azure blob URL, `tfc://<organization>/<workspace>`, or `backend`), so nothing is imported twice.

### 2. Review the selection with the user

`selection.yaml` lists each resource with `include: true` or `false`, and the reason for each exclusion. The default selection leaves out what the cloud creates itself and what another tool manages (CloudFormation, a `ManagedBy` tag, another state). Summarise it for the user by type and scope, point out the exclusions and anything marked `new: true`, and change only what they ask for. They can review it themselves with `infraharvest pick --selection selection.yaml`.

### 3. Import (the user confirms)

Run `import` with the same flags as `discover` and the selection file:

```sh
infraharvest import aws --profile prod --services=vpc,subnet,sg,s3 --regions=eu-west-2 --selection=selection.yaml
```

Over MCP, the `import` tool asks the user to confirm; a client that can't ask returns the command for the user to run. Over the CLI, show the command and wait for the user's go-ahead. Later runs:

- new resources: `discover` again, review what is `new`, then `import ... --selection=selection.yaml --incremental`, with `--managed-state` once `imports.tf` is deleted;
- a run that failed part way: the same command with `--resume`;
- generate the roots again: `--regenerate`.

### 4. Verify and read the report

Each root goes through the verification gate (format, validate, a plan with no changes, the output standard, scanners, secrets, determinism). Read `infraharvest-out/report/report.md` (or the `report` tool, or `infraharvest report`) and tell the user what was imported, what was left out and why, and which secret variables each root needs. After any edit to a root, run `infraharvest verify [output-directory]`, before `imports.tf` is deleted.

### 5. Apply: the user's step

Tell the user how, and stop there:

```sh
cd infraharvest-out/aws/<account>/eu-west-2
terraform init
terraform plan    # imports every resource, changes nothing
terraform apply   # records the resources in the state, without changing them
rm imports.tf
```

Resources in `rejected.hcl` weren't imported; Terraform doesn't load that file.

## Audit

To show what no Terraform state or other tool manages, without importing anything, run `discover` with `--managed-state` pointing at the existing state, and summarise the selection file: what is included is unmanaged, and each exclusion says what manages it. Stop there unless the user wants to import.

## Drift

After the roots are applied, drift shows what changed in the cloud and which changes the next apply would undo. It changes neither the state nor the cloud, but it runs the roots' providers with the user's credentials: run it only on roots the user trusts.

1. Report first: the `drift` tool, or `infraharvest drift [output-directory] --detailed-exitcode`. Show the user the changes.
2. Accept only what the user chooses, with rules (the last rule that matches decides):

```sh
infraharvest drift infraharvest-out --accept 'attribute=tags.*' --keep 'type=aws_security_group'
```

Over MCP, `drift_accept` takes the same rules in `rules` (`accept attribute=tags.*`, `keep type=aws_security_group`) or a `decisions` file, and asks the user to confirm. `--write-decisions` (or `write_decisions`) writes the changes into a decisions file for the user to decide change by change. Accepted changes are edited into the code; the user reviews and applies them.

## Exit codes

| Code | Import | Drift with `--detailed-exitcode` |
|---|---|---|
| 0 | Everything was imported | No drift left to revert |
| 1 | Something couldn't be imported | An accepted change failed verification and wasn't written |
| 2 | The run couldn't start (credentials, Terraform, state) | A root couldn't be checked |
| 3 | Partial, with `--allow-partial` | |
| 4 | | Drift is left that the next apply reverts |
| 130, 143 | Interrupted or terminated | Interrupted or terminated |

On 1 or 3, read `infraharvest-out/report/coverage.json` (`failures`, `directories[].left_out`, `directories[].checks`); on 2, the last line of the log.

## More

- Commands and flags: <https://ignatg.github.io/infraharvest/usage/>
- MCP tools: <https://ignatg.github.io/infraharvest/mcp/>
- Drift: <https://ignatg.github.io/infraharvest/drift/>
- Troubleshooting: <https://ignatg.github.io/infraharvest/troubleshooting/>
