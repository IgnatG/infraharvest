# Drift

Once the roots are applied, people still change things in the cloud: a tag in the console, a security group rule during an incident. `infraharvest drift` finds those changes, and says which ones the next `terraform apply` of the code would undo.

```sh
infraharvest drift [output-directory]
```

It checks every root an import generated in the output directory (default `infraharvest-out`), or those `--root` matches, such as `--root 'aws/*/eu-west-2'`. It changes nothing: not the code, not the state, not the cloud.

## How it works

For each root, with one refresh of the cloud:

1. It initialises the root with its backend (`terraform init -input=false -reconfigure`) and pulls the state (`terraform state pull`).
2. It copies the root into a private directory under `<out>/.infraharvest/drift`, which it removes when done: the state holds secrets. The copy has no `backend.tf`, uses the pulled state as its local state, and calls the same local modules.
3. In the copy, `terraform plan -refresh-only` reads the cloud. Its `resource_drift` lists each resource that differs from the state.
4. It applies that plan to the copy's local state only. A saved plan applies without reading the cloud again.
5. `terraform plan -refresh=false` then compares the code with the refreshed state, again without reading the cloud.

A root whose state has no resources yet isn't refreshed: there is nothing to compare.

## What it reports

Each attribute that changed in the cloud gets an outcome:

| Outcome | Means |
|---|---|
| `reverts` | The code still has the old value: the next apply puts it back. To keep the cloud's value, change the code |
| `absorbed` | The code doesn't set it, such as an attribute the provider computes, or one `ignore_changes` names: the next apply keeps the cloud's value |
| `not_assessable` | It can't be told here, such as a secret whose variable has no value |

A resource deleted in the cloud is reported once, as `deleted`. Some changes are left out: arguments the provider keeps only in state, write-only arguments (`*_wo`), and resources the root still imports. Changes to `tags_all`, `effective_labels` and `terraform_labels` are reported as changes to `tags` and `labels`.

Resources the root's import blocks import, but that the state doesn't have yet, are listed as not applied: they aren't checked.

The report goes into `<out>/report/drift.json` and `<out>/report/drift.md`, apart from the import's report (see [Output](output.md#report)). Values the provider marks sensitive, and values that look like credentials, are shown as `(sensitive value)`. For resources in the root itself, the report also gives the file and line where the code sets the attribute, as best it can tell; for resources in modules it gives none yet.

This command only detects and reports. It doesn't change the code to match the cloud yet.

## Credentials

Drift needs what your own `terraform plan` needs:

- The read-only role the import uses, to refresh the resources.
- Read access to the state: for S3, `s3:GetObject` on the state's key and `s3:ListBucket` on the bucket; for GCS, `storage.objects.get`; for azurerm, read access to the state's blob.

Every plan runs with `-lock=false`. Taking the state's lock needs write access, and drift never writes the state. The catch: a plan without the lock can read the state while someone else is applying. Run drift again after that apply finishes.

Set the root's secret variables with `TF_VAR_` environment variables or an `.auto.tfvars` file. A variable without a value gets a placeholder, and the report lists what it sets as `not_assessable`.

## Backends

S3, GCS and azurerm backends and local state are supported. HCP Terraform (a `cloud` block) and other remote backends, such as `remote`, are refused with an error: their plans run remotely, so drift can't plan a local copy.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Every root was checked. With `--detailed-exitcode`: and no change reverts or is not assessable |
| 2 | A root couldn't be checked, such as one with a remote backend or without access to its state. The others are still checked and reported |
| 4 | With `--detailed-exitcode` only: a change reverts on the next apply, or is not assessable |

Changes the code absorbs alone exit 0.
