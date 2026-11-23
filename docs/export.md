# Export

`infraharvest export` writes the metadata of an import into a file you can hand to a hosted control plane, or keep. It is opt-in and local: it reads the output directory of an import, uploads nothing and makes no network calls. What you do with the file is up to you.

```sh
infraharvest export infrastructure --out-file inventory.json
infraharvest export infrastructure --schema run --out-file run.json
infraharvest export infrastructure --schema run --command drift --request-id 01HZX-7 --out-file run.json
infraharvest export infrastructure --schema gate --out-file gate.json
infraharvest export infrastructure --schema graph --out-file graph.json
infraharvest export infrastructure --schema drift-summary --out-file drift-summary.json
infraharvest export infrastructure --schema ocsf-inventory --out-file ocsf-inventory.json
infraharvest export infrastructure --schema ocsf-findings --out-file ocsf-findings.json
infraharvest export infrastructure --schema run --command discover --out-file run.json
infraharvest export infrastructure --schema ocsf-inventory --command discover --out-file ocsf-inventory.json
infraharvest export infrastructure --schema run --command verify --out-file run.json
infraharvest export infrastructure --schema gate --command verify --out-file gate.json
infraharvest export infrastructure --schema ocsf-findings --command verify --out-file ocsf-findings.json
```

Without `--out-file` the document goes to standard output. The directory defaults to `infrastructure`, as for [`report`](reference/cli.md#infraharvest-report).

## The documents

Each document has a JSON Schema (2020-12, no fields beyond the ones it lists), and they all share one `run_id`, made from the report, so they can be matched. `infraharvest export --schema inventory --print-schema` prints a schema; the files are under [`schemas/`](https://github.com/IgnatG/infraharvest/tree/main/schemas) in the repository.

| `--schema` | What it has |
|---|---|
| `inventory` | The cloud, the scopes (provider, account and region), and for each resource its type, ID, account, region, name, tags and who manages it: `managed` (by a Terraform state, a root of the output, or Terraform's tags), `other_iac` (CloudFormation or another tool) or `unmanaged`, with whether a state, a tag or nothing says so. The counts of the report, in all, by type and by scope. What the export left out (see below). |
| `run` | The command (`import`, `incremental`, `drift`, `discover` or `verify`), the outcome (`ok`, `partial`, `failed` or `interrupted`), when it started and finished if you pass `--started-at` and `--finished-at`, the `request_id` if you pass `--request-id`, and failure codes from a fixed list. For an import or an incremental import also the exit code, the versions of infraharvest and of Terraform or OpenTofu, and the counts. See [The run](#the-run). |
| `gate` | For each check of the [verification gate](output.md#verification-gate) (G1 to G8): whether it passed or was skipped, with reason codes from a fixed list, such as `secrets_found` or `tool_missing`. |
| `graph` | How the imported resources fit together, to draw them: a node for each imported resource, with its type, ID, account and region as the inventory has them, and for each module call, with its registry source and version (`local` or `other` for a module from a path or a URL), each resource in a module naming it as `parent`, and the `environment` of its root, its `address` (`module_address` for a module call) and its `root`, when the import knows them; and an edge (`from`, `to`, the nodes' indexes) for each reference between them that the generated configuration writes, directly or through a local or a data source. Instances of `for_each` and `count` resources refer to the instance with the same key, as `each.key` and `count.index` make them. See [The graph](#the-graph). |
| `drift-summary` | What `infraharvest drift` found, counted: for each root, and each environment of a root [environments share](configuration.md), whether it was checked, how many changes in the cloud there are, by what became of them (`written`, `kept`, `rejected_by_plan`, `rejected_by_check`, `manual`, `secret`, `not_assessable` or `absorbed`, as in [Drift](drift.md#what-it-reports)) and by resource type, and the addresses of the resources that changed. See [The drift summary](#the-drift-summary). |
| `ocsf-inventory` | The inventory's resources as [OCSF](https://ocsf.io) 1.9.0 events of the class Cloud Resources Inventory Info (`class_uid` 5023), one event for each resource, each with its cloud, account and region, its type, ID, name and tags, who manages it, and, for a resource the import took over, the address it has in the configuration, the module call it is in and the path of its root. See [The OCSF inventory](#the-ocsf-inventory). |
| `ocsf-findings` | The checks of the verification gate that failed or were skipped, for each root, as OCSF 1.9.0 Detection Findings (`class_uid` 2004): one event for each check of each root, with the check's name, its reason codes and, for a root an import generated, the `iac` source of the root. No message and no detail of a check. See [The OCSF findings](#the-ocsf-findings). |

No document has an attribute, a value or a message, and only the drift summary, the OCSF inventory and the graph have resource addresses, and the OCSF inventory, the graph and the OCSF findings the paths of the roots: every field is copied by name, and nothing else of the report or the saved inventory is read. The inventory takes its names and tags from what the last `discover` run saved under `.infraharvest/` (the inventories it lists in `.infraharvest/inventory/discover.files`, not those of earlier calls with other regions, profiles or roles that the directory keeps; every inventory there, if the list is missing); without it, the resources are those the report names, without names or tags. Before it is written, each document is checked against its schema, and an export that doesn't match writes nothing.

## What it leaves out

- **Tags that look like secrets.** A tag is dropped when the detectors the verification gate uses flag its key or its value, such as `db_password`, or a GitHub token. The inventory lists the dropped keys (`redaction.tag_keys_dropped`), unless the key is itself a credential.
- **Tags you choose.** `--drop-tag <key>` drops a tag key (repeat it, or separate keys with commas), and `--drop-tags` drops all tags.
- **IDs and names, if you hash them.** `--hash-ids` replaces each ID and name, and the addresses and root paths of the drift summary, the addresses, module addresses and roots of the graph and the addresses, module addresses and root paths of the OCSF inventory, and the root paths and state URLs of the OCSF findings, with its HMAC-SHA256 in hexadecimal. The key is yours: it comes from the environment variable `INFRAHARVEST_EXPORT_KEY`, or from a file with `--hash-key-file`, and must have at least 16 bytes. Nothing writes it down. The same key gives the same hash for a resource in every export, so the control plane can follow it over time; without the key, nobody can check a guess. `--hash-ids` without a key is an error. With it, tags don't give the ID or name away either: the `Name` tag's value, and a value that is or holds the resource's ID or name (one of 4 characters or more), such as an ARN, are hashed with the same key, and a tag whose key holds one, such as `kubernetes.io/cluster/<name>`, is dropped without being listed. Other tags can still name things, such as `aws:cloudformation:stack-name`: drop them with `--drop-tag`.
- **Addresses, if you drop them.** `--drop-addresses` leaves the resource addresses and the paths of roots out of the documents that have them, which are the drift summary, the graph (which loses its nodes' roots too), the OCSF inventory, which also loses its module addresses, and the OCSF findings, whose events lose the path of the root and its state; the counts, the OCSF inventory's resources and the findings themselves stay. It is for a control plane that needs to know how much drifted, or what exists, not how the code names it. With `--hash-ids`, the addresses are hashed with the same key instead, so an address in one export is the same hash in the next. A hashed address doesn't join the inventory's hashed ID: the two hash different text.

```sh
export INFRAHARVEST_EXPORT_KEY="$(openssl rand -hex 32)"   # keep it: another key hashes differently
infraharvest export infrastructure --hash-ids --drop-tag owner-email --out-file inventory.json
infraharvest export infrastructure --schema drift-summary --drop-addresses --out-file drift-summary.json
infraharvest export infrastructure --schema ocsf-inventory --hash-ids --out-file ocsf-inventory.json
```

Resource types, accounts, regions and scopes, and the counts, stay readable: they are what the control plane groups by. Account IDs are not hashed.

## Notes

- The export describes the last import in the directory. It adds the resources `discover` listed that the report doesn't name, as unmanaged.
- **Times.** The report holds no timestamps, so that its bytes, and the `run_id` made from them, name the run. `--started-at` and `--finished-at` (RFC 3339, such as `2026-11-03T09:30:00Z`; another offset is converted to UTC) say when the import ran: the run document gets them as `started_at` and `finished_at`, and `--finished-at` is also the `generated_at` of the inventory, the graph and the drift summary. Without them `started_at` and `finished_at` are left out and `generated_at` is the time of the export, so exporting the same run again moves it. `--finished-at` may not be before `--started-at`. The [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md#sending-a-run-to-a-control-plane) records them around the import and passes them on.
- A resource whose ID looks like a credential stops the export, and so does an address, or the path of a root, in the drift summary and the OCSF inventory, unless `--drop-addresses` leaves them out.

## The run

The run document describes the last run of a command that leaves something in the output directory to describe it by: `import` writes `report.json`, and so does an incremental import (`--incremental`, which adds to the roots the output directory has), which is told apart by naming it, `drift` writes `drift.json`, `discover` saves the inventories it listed under `.infraharvest/inventory/`, and `verify` writes `_reports/verify.json`. The export reads the command from the directory for the first two: with only `drift.json` it describes the drift run, else the import. A directory that has also run `discover` or `verify` is still described as the import or the drift run; those two are named with `--command discover` or `--command verify`. A directory that has both, as when you import and check for drift later, is described as the import, as it always was; `--command drift` describes the drift run instead. `--command incremental` describes the same report as `import` does, with the same fields and the same `run_id`, and only the command differs; it is for a run that imported with `--incremental`, which the report doesn't say itself, so you say it. `--command import` or `--command incremental` without a `report.json`, or `--command drift` without a `drift.json`, is an error. `incremental` is for the run document only, as `import` is.

A drift run is `ok` when every root was checked, `partial` when some couldn't be, with the failure code `roots_not_checked`, and `failed` when none could. It is also `partial`, with the failure code `check_failed`, when an accepted change was rejected (`rejected_by_plan` or `rejected_by_check`), so that nothing was written for it: `infraharvest drift --detailed-exitcode` exits 1 for it, and the Action's step fails. Drift found in the cloud isn't a failure of the run. It has no exit code, as that depends on `--detailed-exitcode`, and no versions or counts: those are an import's. Its `run_id` is made from `drift.json`, as an import's is from `report.json`, so it differs from the import's.

A discover run is `ok` when every listing worked, `partial` when some failed in part, and `failed` when none listed anything, both with the failure code `run_failed`. A verify run is `ok` when every root was checked and every check passed, `failed` when a check failed (`check_failed`) or no root could be checked, and `partial` when some roots couldn't be checked and no check failed (`roots_not_checked`, which a `failed` run has too if a root couldn't be checked). Neither has an exit code, versions or counts, as a drift run has none. A discover run's `run_id` is made from the inventories it saved, and a verify run's from `verify.json`, so the same results are the same run; `ocsf-inventory` (of a discover run) and `gate` (of a verify run) have it too. The OCSF inventory of a discover run lists what was listed, in the same form as an import's, but the saved inventories say nothing of who manages a resource, so each is `unmanaged` with the basis `none` and has no `iac`; the selection file is where discover records which resources the default rules leave out. The gate of a verify run is made from `verify.json` as an import's is from the report, and doesn't pass if a root couldn't be checked.

`--request-id <id>` puts a `request_id` into the run document: the ID a control plane gave to ask for the run, so that it can match the run to its request. It is 1 to 128 letters, digits, `.`, `_`, `:` and `-`. No other document has it.

## The drift summary

`drift-summary` is made from `_reports/drift.json`, which `infraharvest drift` writes, so it needs a directory that has been checked for drift; it doesn't read the import's report. Drift writes values into `drift.json`, and the export never reads them: it takes each change's root, environment, address, resource type and outcome, and nothing else, so no attribute and no value, not even the name of the attribute, reaches the document. Its `run_id` is made from `drift.json`, as the run document of a drift run's is, so the two match. `generated_at` is the time of the export, or `--finished-at`.

Each root is listed with the environment it was checked as, if it is one [environments share](configuration.md), and `checked`: `false` for a root drift couldn't check, which has no changes counted, and whose error is not in the document. Each root has its changes counted by outcome and by resource type, with `outcomes` having every outcome of [Drift](drift.md#what-it-reports), so a control plane can say how many changes the next apply reverts (`kept`), how many need a hand edit (`manual`, `rejected_by_plan` and `rejected_by_check`) and how many are secrets. A change of tags that several resources get from one place, such as `local.tags`, has no resource type: it is counted under the type `shared_tags`, and the resources it covers are in the root's `addresses`, not the place that sets the tags. `drift_found` is the report's: whether a change is left that the next apply reverts or that can't be assessed.

`addresses` lists the resources that changed in each root, once each, sorted, as Terraform names them, such as `module.db.aws_db_instance.main` or `aws_s3_bucket.named["logs"]`. An address can hold the key of a `for_each`, which can be a name: hash the addresses with `--hash-ids`, which is the same HMAC under the same key as the inventory's, or leave them out with `--drop-addresses`, which wins when both are given. `redaction.addresses` says which was done: `plain`, `hashed` or `dropped`. An address that looks like a credential stops the export. A root's `root` is its path in the output directory: `--hash-ids` hashes it with the addresses, and `--drop-addresses` leaves it out, so the roots are told apart only by their environment and order. Environments aren't hashed, and a path or environment that looks like a credential stops the export.

## The OCSF inventory

`ocsf-inventory` has the resources of the inventory, the same ones in the same order, for a consumer that reads [OCSF](https://ocsf.io) (the Open Cybersecurity Schema Framework), such as a security data lake. It is one JSON document, `{"schema": "infraharvest/ocsf-inventory/v1", "run_id": ..., "ocsf_version": "1.9.0", "redaction": {...}, "events": [...]}`, with the same `run_id` as the others, and its schema ([`ocsf-inventory-v1.schema.json`](https://github.com/IgnatG/infraharvest/blob/main/schemas/ocsf-inventory-v1.schema.json)) is closed, like theirs. It describes the subset of OCSF that infraharvest writes, not the whole schema.

`redaction` says what was left out or hashed, as the inventory's does, so that a receiver can tell a hashed ID from a real one: `tag_keys_dropped` (the keys of the tags that were dropped, unless the key is itself a credential), `all_tags_dropped` (present, and `true`, with `--drop-tags`), `ids_hashed` and `hash_alg` (`hmac-sha256`), which cover the IDs, names, addresses, module addresses, roots and paths alike, and `addresses_dropped`, `true` with `--drop-addresses`, which leaves the addresses, module addresses and paths out.

Each event is of the class Cloud Resources Inventory Info (`class_uid` 5023, category Discovery, `category_uid` 5), with the activity Collect (`activity_id` 2, so `type_uid` 502302), `severity_id` 1 (Informational), and `time` in milliseconds since the epoch: the time the inventory's `generated_at` has, so `--finished-at` fixes it. `metadata` has the OCSF `version`, the `product` (infraharvest and its version), the `profiles` `cloud` and `iac/iac_source`, the `extensions` entry of the [extension](#the-iac-extension) below, and `correlation_uid`, the `run_id`. `cloud` has the `provider` (`AWS`, `Azure` or `GCP`), the `account` (its `uid`; account IDs are never hashed) and the `region`. `resources` has one `resource_details` object:

| Field | Value |
|---|---|
| `type`, `uid`, `name` | The resource's type, ID and name, as the inventory has them; `--hash-ids` hashes `uid` and `name` with the same key as the inventory's, so the two documents join on type and ID. |
| `region` | The region of the scope, if it has one. |
| `tags` | The inventory's tags, as OCSF's name and value objects, sorted by name, after the same redaction: tags that look like secrets, `--drop-tag` and `--drop-tags`. |
| `labels` | `status:` and `status_basis:` with the inventory's `status` and `status_basis`, and `owner:` with its `owner`, if it has one. OCSF's `owner` is a user, so who manages a resource is a label. As in the inventory, the status is what [`discover`](reference/cli.md#infraharvest-discover) found: a resource the import has since taken over is still `unmanaged` there, and its `iac` says where it is now. |
| `iac` | Where the import wrote the resource's configuration, if it took it over (see below). |

An event for each resource, not for each account and region, because OCSF's `cloud` object holds one provider, account and region, so a batch couldn't span the regions of an account; each resource has its own `iac` source, which a batch would repeat; and a consumer receives events of a bounded size, however large the estate is. The schema allows more than one entry in `resources` for a consumer that batches, but infraharvest writes one.

### The iac extension

The `iac_source` profile is infraharvest's own OCSF extension, `iac` (`uid` 999), under [`schemas/ocsf/iac/`](https://github.com/IgnatG/infraharvest/tree/main/schemas/ocsf/iac), in the layout OCSF's extensions have, so that `ocsf-schema-compiler path/to/ocsf-schema -e schemas/ocsf/iac` compiles it with the core schema. It adds the object `iac_source`, as the attribute `iac`, to the object `resource_details`, with:

| Field | What it is | Filled by export |
|---|---|---|
| `tool` | `terraform` or `opentofu` | Yes, from the engine the import ran. |
| `address` | The resource's address in the configuration, such as `aws_s3_bucket.logs` or `module.db.aws_db_instance.main` | Yes. |
| `module_address` | The module call the resource is in, such as `module.db`, or `module.a["x"].module.b` for nested calls | Yes, for a resource in a module. |
| `repo_url` | The repository that holds the configuration | If you pass `--repo-url`, or, for this document only, `GITHUB_SERVER_URL` and `GITHUB_REPOSITORY` are set, as in GitHub Actions. An `http` or `https` URL without credentials, a query or a fragment; a trailing `/` or `.git` is dropped. |
| `commit_sha` | The commit the configuration is from, 40 or 64 lowercase hexadecimal digits | If you pass `--commit-sha`, or `GITHUB_SHA` is set. |
| `path` | The directory of the resource's root, relative to the output directory, such as `aws/123456789012/eu-west-2` | Yes. |
| `state_key` | Where the root's state is, as `--managed-state` names state objects: `s3://<bucket>/<key>`, `gs://<bucket>/<prefix>/default.tfstate`, or the `https://` URL of an Azure blob | If you pass `--config` with the import's configuration file and it has a `backend`. |
| `outcome_id`, `outcome` | What the import did with the resource: `1` `Written` (its configuration was written), `2` `Left Out` (it couldn't be imported or generated), `3` `Excluded` (the selection, or its owner, left it out), or `4` `Not Imported` (listed, but not taken on) | Yes, for every resource. |

Every resource has an `iac` source, with its `outcome`. The rest is only for a resource the import took over, with `Written`: the export doesn't know where a resource it found managed by a state, a tag or CloudFormation is written. Its `status` label stays what discovery found, `unmanaged`, as it is until the configuration is merged and applied; `Written` is what tells it apart from a resource no one has taken on. `repo_url` and `commit_sha` are written to each of those that has one, and are not hashed: you name them. `--drop-addresses` leaves `address`, `module_address`, `path` and `state_key` out, and `--hash-ids` hashes them as it does the drift summary's addresses, so an address is the same hash in both documents, and the OCSF inventory's from one export to the next. An address, a path or a state URL that looks like a credential, as a key of a `for_each` can, stops the export. infraharvest keeps a state for each root and doesn't use Terraform workspaces, so the source has no workspace.

The extension isn't in the [OCSF Extensions Registry](https://github.com/ocsf/ocsf-schema/blob/main/extensions.md) yet, so it has the registry's development `uid`, 999, which is meant for extensions that aren't registered. A consumer tells it apart by its `name` and `version`, which `metadata.extensions` gives with the `uid`.

## The OCSF findings

`ocsf-findings` says which checks of the [verification gate](output.md#verification-gate) failed or were skipped, and where, for a consumer that reads OCSF and wants them as findings. The `gate` document says whether a check passed over all the roots; this one has an event for each check of each root that it didn't pass. It is one JSON document, `{"schema": "infraharvest/ocsf-findings/v1", "run_id": ..., "ocsf_version": "1.9.0", "redaction": {...}, "events": [...]}`, closed like the others ([`ocsf-findings-v1.schema.json`](https://github.com/IgnatG/infraharvest/blob/main/schemas/ocsf-findings-v1.schema.json)), with the `run_id` of the import's report, or of `verify.json` with `--command verify`, which are the ones the `gate` document has. A gate that passed everywhere has no events.

An event is made for a check that failed, or whose runs were all skipped, in a root, such as a scanner that isn't installed. A check that passed is not a finding, and neither is a root that couldn't be checked at all: the run document says that, with `roots_not_checked`. An import's roots are its directories and the merged roots of its stacks, in the order of the report; a verify run's, in the order of `verify.json`.

| Field | Value |
|---|---|
| `class_uid`, `category_uid`, `activity_id`, `type_uid` | Detection Finding `2004`, category Findings `2`, activity Create `1`, so `200401`. |
| `severity_id`, `severity`, `is_alert` | A failed check is Medium (`3`), and High (`4`) for `G6 secrets`; both are alerts. A skipped check is Low (`2`) and not an alert. |
| `status_id`, `status` | `1`, New. |
| `time` | Milliseconds since the epoch: the inventory's `generated_at`, so `--finished-at` fixes it. |
| `message` | The check's name, `failed` or `warned`, and its reason codes, such as `G6 secrets failed: secrets_found`. |
| `finding_info` | `uid` is `<run_id>:<check ID>:<root number>`, the root's place in the run, counted from 1, so the same results give the same uids; `title` is the check's name, one of `G1 format` to `G8 moves`; `types` are its reason codes, from the list the [gate](output.md#verification-gate) document has. |
| `metadata` | As the OCSF inventory's: the OCSF version, the product, the profiles `cloud` and `iac/iac_source`, the extension, and `correlation_uid`, the `run_id`. |
| `cloud` | For an import's root: the provider, and the account and region its resources were listed in, if the report says. A verify run has none. |
| `resources` | The root the check ran on, as `{"type": "iac_root", "uid": <its path>}`, and for a root an import generated its `iac` source. |

Only the check's name and its reason codes are used, never what the check says in its details, which can hold addresses and values; the details only choose which of a fixed list of reason codes the check gets. The root's `iac` source is the [extension's](#the-iac-extension) with the `outcome` `Written` (an import writes the configuration of every root it checks), the `tool`, the `path` of the root, the `state_key` if you pass `--config` and it has a `backend`, and the `repo_url` and `commit_sha` as for the OCSF inventory; it has no `address`, as the root isn't a resource. A verify run's roots have a path and no `iac`, since verify doesn't say what an import did.

`redaction` has `ids_hashed`, `hash_alg` and `addresses_dropped`; the events have no tags. `--hash-ids` hashes the root's path, in `uid` and in `iac.path`, and its state, with the same key and so to the same hash as the OCSF inventory's `path`; `--drop-addresses` leaves them out, and with them the `uid`, and the `resources` of a verify run, which have nothing else. A path that looks like a credential stops the export unless `--drop-addresses` leaves it out.

## Closed schemas

Every export schema is closed: each object has `additionalProperties: false`, and the lists of codes are closed lists. That is how the schemas enforce that no names or values are sent, since a field nobody listed can't carry one, so they stay closed, and a consumer can't be told to ignore what it doesn't know: its copy of a closed schema refuses a document with a new field.

So a new field ships in every consumer before infraharvest emits it: each consumer, a control plane and Terragram, vendors the new schema first, and only then does a release of infraharvest write the field. Each consumer checks its vendored copies against infraharvest's `schemas/` directory in its CI, so that a copy that falls behind fails there and not in a customer's run. Until a consumer has the new schema, its copy refuses the field even though the schema marks it optional.

## The graph

The graph is drawn from the configuration of each root, so it shows what the code says: a subnet's `vpc_id = aws_vpc.main.id` is an edge from the subnet to the VPC. It has the resources the import took over, not the ones it left out or excluded. References between roots go through variables (see [Output](output.md)), and references inside a module's own code are the module's, so neither is an edge; the roots of [environments](configuration.md) merged into a root they share have their nodes, with the addresses of their own roots, but no edges yet: the merged root names their resources at other addresses. The same output gives the same graph, nodes and edges in the same order, so two graphs can be compared.

`--hash-ids` hashes a node's ID with the same key as the inventory's, so the graph and the inventory join on type and ID. The graph has no tags, so `--drop-tag` and `--drop-tags` don't change it.

A node may also have these fields, all optional (see the schema's `description`; the document is still `infraharvest/graph/v1`, whose schema is closed and lists them). A consumer whose copy of the schema is closed, as the schemas of every export document are, doesn't accept them until it has the schema that lists them (see [Closed schemas](#closed-schemas)):

| Field | Of | What it is |
|---|---|---|
| `environment` | any node | The environment of the root the node is in, such as `prod`, from the [environments](configuration.md) the import merged or kept apart. Left out when the root has none. Never hashed, as in the drift summary. |
| `address` | a resource | The resource's address in the configuration, such as `aws_vpc.main` or `module.net.aws_vpc.main`, as the OCSF inventory's `iac.address` has it. For a resource of an environment merged into a root it shares, the address in the environment's own root, from before the merge, as the OCSF inventory has it, so that a finding from either export lands on the same node. |
| `module_address` | a module call | The module call's address in the root, such as `module.net`, as the OCSF inventory's `iac.module_address` has it for a resource in the module. The same for the module calls of a merged environment. |
| `root` | any node | The root the node's resource or module call was generated in, as its path in the output directory, such as `aws/123456789012/eu-west-2`, as the OCSF inventory's `iac.path` has it. It tells apart the same address in two roots of one environment, such as the same bucket name in two regions. For a node of an environment merged into a root it shares, the environment's own root, from before the merge, as for `address`. It is redacted as `address` is: hashed with `--hash-ids`, with the same key and so to the same hash as the OCSF inventory's `path`, and left out with `--drop-addresses`, as `redaction.addresses` says. A consumer needs the schema that lists it first (see [Closed schemas](#closed-schemas)). |

`--hash-ids` hashes `address`, `module_address` and `root` with the same key as the IDs, so a hashed address or root is the same hash as the OCSF inventory's and in every export; `--drop-addresses` leaves them out, and wins when both are given. `redaction.addresses` says which was done: `plain`, `hashed` or `dropped`, as in the drift summary. An address or a root that looks like a credential, or an environment that does, stops the export, unless `--drop-addresses` leaves the addresses out. Nothing of what a resource holds goes with an address: no attribute and no value.

## Control plane API

The [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md#sending-a-run-to-a-control-plane) sends the documents of the run to a control plane when `control-plane-url` is set: the OCSF inventory, run, gate and graph documents of an import, and the drift summary of a drift check (with the run document of the drift check, `--command drift`, when no import ran). `infraharvest export` itself never does. A server that implements the following can receive them; `<base>` is the URL as given, without a trailing slash, and may have a path.

| Request | Body |
|---|---|
| `POST <base>/api/v1/session` | empty; opens a session (see below) |
| `POST <base>/api/v1/ingest/ocsf-inventory` | an `ocsf-inventory/v1` document |
| `POST <base>/api/v1/ingest/run` | a `run/v1` document |
| `POST <base>/api/v1/ingest/gate` | a `gate/v1` document |
| `POST <base>/api/v1/ingest/graph` | a `graph/v1` document |
| `POST <base>/api/v1/ingest/drift-summary` | a `drift-summary/v1` document |

The Action doesn't upload `inventory/v1` or `ocsf-findings`: they stay documents `infraharvest export` writes. The OCSF inventory has the redaction flags the Action is given (tags dropped, IDs and addresses hashed, addresses dropped), and says which in its `redaction` block, and the repository and the commit from the job's `GITHUB_SERVER_URL`, `GITHUB_REPOSITORY` and `GITHUB_SHA`.

- **Session.** First, the Action sends the job's GitHub Actions OIDC token, requested for the audience in `control-plane-audience` (default `infraharvest-api`), in the header `X-GitHub-OIDC-Token` of `POST <base>/api/v1/session`, with an empty body. Verify it against GitHub's keys (`https://token.actions.githubusercontent.com/.well-known/jwks`): the issuer, the audience, the expiry, and the `repository`, `ref` and `sub` claims that say which workflow sent it. A `200` answers `{"token": "<run token>", "expires_at": "<RFC 3339 time>"}`, and may add `"limits": {"max_body_bytes": <n>, "max_decompressed_bytes": <n>}` (see Sizes). The OIDC token goes to this endpoint only; the uploads carry the run token. The Action masks both and doesn't print them.
- **Order.** The documents are sent one after another after the session, and the next is not sent if one fails: an import's OCSF inventory, run, gate and graph, in that order; a drift check's run document (when no import ran) and then the drift summary, last of all. The documents of an import share a `run_id`; the drift summary has the drift check's own, which its run document has too. Sending a document again (after a retry) must give the same result: key it by `run_id` and the endpoint.
- **Headers.** `Authorization: Bearer <run token>`, from the session; `Content-Type: application/json`; `Content-Encoding: gzip`; and `Idempotency-Key: <run_id>:<document>:<GITHUB_RUN_ID>`, such as `6f1c9d3e-0a52-5b8e-9c47-2d1f3a5b7c90:ocsf-inventory:12345678`, the same for each send of a document of a workflow run, its re-runs included. A `run_id` comes from what the run found, so two workflow runs that find the same have the same one; the workflow run's ID keeps them apart. Outside GitHub Actions the key is `<run_id>:<document>`.
- **Body.** The document, as `infraharvest export` wrote it, gzip-compressed, so decompress it before reading it as JSON; validated against its schema ([`schemas/`](https://github.com/IgnatG/infraharvest/tree/main/schemas)). A server should check it again.
- **Response.** Any `2xx` means the document was accepted; the body is ignored (`201` or `204` are fine). Any `4xx` other than `429` fails the run without a retry, such as `401` and `403` (token refused), `400` and `422` (document refused), and `402` (the organisation's plan refused it, such as for being over its cloud-account limit). The same goes for the session request. Say why in a JSON body, `{"error": "..."}`: the Action prints it, or the start of the body if it isn't JSON. `429` and `5xx` are retried up to four times in all, after 2, 4 and 8 seconds, so a server that is overloaded can say so; a `429` whose JSON body has `retryAfterMs`, or that has a `Retry-After` header in seconds, is waited for that long instead, up to 60 seconds. A connection that is refused or reset, a name that doesn't resolve and a timeout are retried the same way; a TLS or protocol error is not. Redirects are not followed.
- **Sizes.** The Action logs the size of each document in bytes, as JSON and gzipped, one line each (see [the Action's README](https://github.com/IgnatG/infraharvest/blob/main/action/README.md#sending-a-run-to-a-control-plane)), so a control plane can compare what it receives with what was sent. A control plane should take a document of 2,000,000 bytes gzipped and 16,000,000 once decompressed at least. The Action uses those as its limits, unless the session answer has `limits` with `max_body_bytes` (what a request may be as sent, gzipped) and `max_decompressed_bytes` (what a document may be once decompressed), each a whole number from 1000 to 100,000,000: then it uses those, so a control plane can say what it takes and change it without a new Action. Limits that are missing keep the defaults, and ones that are not valid are ignored with a warning. The Action warns when one is within 10% of either, and fails before sending any document when one is over, so a run is never left half uploaded for its size.
- **Transport.** The Action accepts `https://` URLs only, and `http://` for `localhost`, `127.0.0.1` and `[::1]`, for a server run in the same job.
