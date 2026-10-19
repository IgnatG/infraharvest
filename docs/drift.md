# Drift

Once the roots are applied, people still change things in the cloud: a tag in the console, a security group rule during an incident. `infraharvest drift` finds those changes, and says which ones the next `terraform apply` of the code would undo. With `--accept`, it writes the cloud's values of the changes you choose into the code instead.

```sh
infraharvest drift [output-directory]
infraharvest drift [output-directory] --accept 'attribute=tags.*'
```

It checks every root an import generated in the output directory (default `infraharvest-out`), or those `--root` matches, such as `--root 'aws/*/eu-west-2'`. It never changes the state or the cloud. Without `--accept`, it doesn't change the code either.

## How it works

For each root, with one refresh of the cloud:

1. It initialises the root with its backend (`terraform init -input=false -reconfigure`) and pulls the state (`terraform state pull`).
2. It copies the root into a private directory under `<out>/.infraharvest/drift`, which it removes when done: the state holds secrets. The copy has no `backend.tf`, uses the pulled state as its local state, and calls the same local modules.
3. In the copy, `terraform plan -refresh-only` reads the cloud. Its `resource_drift` lists each resource that differs from the state.
4. It applies that plan to the copy's local state only. A saved plan applies without reading the cloud again.
5. `terraform plan -refresh=false` then compares the code with the refreshed state, again without reading the cloud.

A root whose state has no resources yet isn't refreshed: there is nothing to compare. Accepting changes (see below) plans the copy again, still with `-refresh=false`: the cloud is read once per root.

## What it reports

Each attribute that changed in the cloud gets an outcome:

| Outcome | Means |
|---|---|
| `kept` | The code still has the old value: the next apply puts it back. This is what happens to every change you don't accept |
| `written` | You accepted it, and the cloud's value is now in the code |
| `manual` | You accepted it, but where the code sets the value takes a hand edit, such as an expression or a module call; `reason` says why |
| `rejected_by_plan` | You accepted it, but the plan of the edited code didn't take the edit, so nothing was written for it; `reason` says what the plan did |
| `secret` | You accepted it, but it is a secret, which is never written into the code |
| `not_assessable` | It can't be told here, such as a secret whose variable has no value |
| `absorbed` | The code doesn't set it, such as an attribute the provider computes, or one `ignore_changes` names: the next apply keeps the cloud's value |

Each change also has a `decision`: `accept` when an `--accept` rule chose it, else `keep`. A change whose old value the next apply can only put back by replacing the resource has `forces_replacement`, and its reason says so: accepting it is how to keep the resource.

A resource deleted in the cloud is reported once, as `deleted`. Some changes are left out: arguments the provider keeps only in state, write-only arguments (`*_wo`), and resources the root still imports. Changes to `tags_all`, `effective_labels` and `terraform_labels` are reported as changes to `tags` and `labels`. A tag that every resource gets from one place, the provider's default tags or a shared `local.tags`, and that changed the same way on all of them, is reported once, for that place (see [Tags several resources share](#tags-several-resources-share)).

Resources the root's import blocks import, but that the state doesn't have yet, are listed as not applied: they aren't checked.

The report goes into `<out>/report/drift.json` and `<out>/report/drift.md`, apart from the import's report (see [Output](output.md#report)). `drift.md` lists the changes accepted into the code (with the file and line), those kept, which the next apply reverts, those that need a hand edit, the secrets and those not assessable, then those absorbed. Values the provider marks sensitive, and values that look like credentials, are shown as `(sensitive value)`. For resources in the root itself, the report gives the file and line where the code sets the attribute, as best it can tell; for resources in modules it gives none yet.

## Accepting changes

`--accept <rule>` takes the cloud's values of the changes the rule matches into the code. `--keep <rule>` keeps the code's values, which the next apply puts back. Both repeat, and the last rule that matches a change decides; a change no rule matches is kept. A rule is:

| Rule | Matches |
|---|---|
| `all` | Every change |
| `attribute=<pattern>` | Changes to these attributes, such as `attribute=tags.*` or `attribute=ingress*` |
| `type=<pattern>` | Changes to resources of these types, such as `type=aws_security_group` |
| `address=<pattern>` | Changes to these resources, such as `address=aws_s3_bucket.logs` |
| `root=<pattern>` | Changes in these roots, by their path in the output directory, such as `root=aws/*/eu-west-2` |

`*` matches any text. For example, `--accept all --keep 'type=aws_security_group'` accepts everything but security group changes.

A change of tags several resources share (see [below](#tags-several-resources-share)) has the address of where the root keeps them, such as `local.tags`, and lists the resources in `covers`. `attribute=` and `root=` rules match it as any other change. A `type=` rule matches it if one of the resources it covers is of that type; an `address=` rule, if its own address or one of theirs matches. So `--accept 'type=aws_s3_bucket'` accepts a change of `local.tags` that covers a bucket and a queue, editing the tag for both; to keep it, add `--keep 'address=local.tags'` after it.

An accepted change is edited in place, keeping comments and the layout of everything else, and only the edited files are formatted:

| Where the code sets the value | What accepting it writes |
|---|---|
| A literal, such as `"prod"`, or a key of a map or object, such as `Env` in `tags = { Env = "prod" }` | The cloud's value in its place, typed as the provider's schema says: `port = 80`, not `"80"`. A list is written whole, as the provider may order a set's elements its own way |
| Nowhere: the code leaves the argument out | The argument, where the layout puts it in the block. A tag key the code doesn't have goes into the resource's own `tags` |
| A local that only this resource uses | The local's value |
| A local that other resources use too | The value at this resource, so the others keep theirs: `merge(local.tags, { Env = "dev" })` for a key, else the value in place of `local.x`. A key all those resources changed the same way is edited in the local once (see [below](#tags-several-resources-share)) |
| A tag from the provider's `default_tags` or `default_labels` | The tag in the resource's own `tags` or `labels`, whose value wins over the provider's; or, if every resource that gets it changed it the same way, in the provider's tags once |
| `merge(...)`, as tags often are | The key in the resource's own map, the last argument, whose values win |
| A reference, such as `aws_vpc.main.id` | A reference to another resource the root manages, if the cloud's value is its ID, ARN or name; else a hand edit |
| A variable with a default, such as another root's value | Nothing: update it from the root it comes from |
| A sensitive variable | Nothing: update it in your secret store, or `TF_VAR_<name>` |
| A function other than `merge`, a conditional, a `for` expression, a `dynamic` block, or a block with `count` or `for_each` | Nothing: a hand edit, at the file and line the report gives |

When the cloud's value is the default the catalogue names for an argument, such as an S3 bucket's `STANDARD` storage class, the argument is removed rather than written. A key or an argument the cloud no longer has is removed.

Before anything is written into the root, the edits are made in the private copy and planned again without refreshing. Each accepted resource must then plan no change to what was accepted, and no change the plan of the code didn't already have, apart from secrets and arguments kept only in state. Every other resource must plan the same change as before: that catches an edit of a local that reaches another resource. The plan's errors and differences are traced back to the edits that caused them; those are left out as `rejected_by_plan`, and the rest are planned again. Then the edited copy must pass the format, validate, standards, secrets and moves checks of the [verification gate](output.md#verification-gate). Only then are the files written into the root, and if writing one fails, those already written are restored, so the root is left as it was.

Look the edits over, commit them, and apply as usual: the plan shows no change for what was written.

### Tags several resources share

Generated roots keep the tags most resources share in one place:

| Cloud | Where | How the resources get them |
|---|---|---|
| AWS | `local.tags`, in `locals.tf` | The provider's `default_tags { tags = local.tags }`. Each resource's `tags` has only its own tags, and its own values of shared keys, which win. A resource without some of the shared tags uses the `aws.without_default_tags` provider, with its tags written out |
| Azure | `local.tags`, in `locals.tf` | Each resource's `tags = local.tags`, or `merge(local.tags, { ... })` with its own tags over them |
| Google | `local.labels`, in `locals.tf` | The provider's `default_labels = local.labels`. Each resource's `labels` has only its own |

A root written by hand can keep them in the provider block itself, as a map literal: that is then the place, such as `provider.aws.default_tags`.

When every resource that gets a key from that place changed it in the cloud to the same value, or removed it, that is one change: its address is the place's, such as `local.tags`, its attribute the key's, such as `tags.Env`, and `covers` lists the resources. `drift.md` lists it once. Accepting it edits the place once: the key's value is replaced, or the key removed. Which resources get a key is told from the code, for those whose tags refer to the local, and from the state, for those the provider tags: a resource whose `tags_all` (or `terraform_labels`) has the provider's value of the key, and whose own `tags` (or `labels`) don't set it. A local that anything else refers to, such as a module call or an output, or that a second configuration of the provider applies too, isn't treated as shared: each resource's change is its own.

When only some of those resources changed the key, or to different values, each change is its own, accepted at its resource: the key in the resource's own `tags` or `labels`, or `merge(local.tags, { Env = "dev" })` where the resource refers to the local. A resource can't leave out one of the provider's default tags, so a key removed from only some of the resources that get it from `default_tags` or `default_labels` is a hand edit, and the reason says so.

Planned without refreshing, an edited root can still change a resource's own `tags` or `labels` while the attributes that merge them with the provider's, `tags_all`, or `terraform_labels` and `effective_labels`, keep the key as it was: what reaches the cloud is the same, and only the state records the change. Verification doesn't count that as a change. With AWS, the next `terraform plan`, which refreshes, shows nothing for it. With Google, the next plan of a label accepted at a resource shows the label added to the resource's `labels`: applying it changes only the state, not the cloud.

The edit of the shared tags is verified as any other: each resource it covers must plan no change to the key, and every other resource must plan what it did before, which catches the edit reaching a resource it doesn't cover. If the plan rejects the edit, it is left out, and each resource's change is accepted at the resource instead, with a reason that says why.

### Secrets

A value the provider marks sensitive, or one infraharvest would take for a secret (see [Secrets](output.md#secrets)), is never written into the code or the report. Accepting it reports it as `secret`: where the code reads it from a sensitive variable, update the secret store or the `TF_VAR_` variable; elsewhere, move it into a sensitive variable by hand.

### What is a hand edit for now

- Resources in module calls: the report names the call; the call's arguments are edited by hand.
- A nested block added or removed in the cloud, such as a new `ingress` rule block.
- A resource deleted in the cloud: remove it from the code, or apply to create it again.

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
| 0 | Every root was checked. With `--detailed-exitcode`: and no change is left that the next apply reverts or that is not assessable |
| 1 | With `--detailed-exitcode` only: an accepted change failed verification (`rejected_by_plan`), so it wasn't written |
| 2 | A root couldn't be checked, such as one with a remote backend or without access to its state. The others are still checked and reported |
| 4 | With `--detailed-exitcode` only: a change is left that the next apply reverts (kept, a hand edit or a secret), or that is not assessable |

Changes written into the code, and those the code absorbs, exit 0. A root that couldn't be checked comes first (2), then a rejected edit (1), then drift left (4).
