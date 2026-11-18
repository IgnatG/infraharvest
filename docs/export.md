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
```

Without `--out-file` the document goes to standard output. The directory defaults to `infrastructure`, as for [`report`](reference/cli.md#infraharvest-report).

## The documents

Each document has a JSON Schema (2020-12, no fields beyond the ones it lists), and they all share one `run_id`, made from the report, so they can be matched. `infraharvest export --schema inventory --print-schema` prints a schema; the files are under [`schemas/`](https://github.com/IgnatG/infraharvest/tree/main/schemas) in the repository.

| `--schema` | What it has |
|---|---|
| `inventory` | The cloud, the scopes (provider, account and region), and for each resource its type, ID, account, region, name, tags and who manages it: `managed` (by a Terraform state, a root of the output, or Terraform's tags), `other_iac` (CloudFormation or another tool) or `unmanaged`, with whether a state, a tag or nothing says so. The counts of the report, in all, by type and by scope. What the export left out (see below). |
| `run` | The command (`import` or `drift`), the outcome (`ok`, `partial`, `failed` or `interrupted`), when it started and finished if you pass `--started-at` and `--finished-at`, the `request_id` if you pass `--request-id`, and failure codes from a fixed list. For an import also the exit code, the versions of infraharvest and of Terraform or OpenTofu, and the counts. See [The run](#the-run). |
| `gate` | For each check of the [verification gate](output.md#verification-gate) (G1 to G8): whether it passed or was skipped, with reason codes from a fixed list, such as `secrets_found` or `tool_missing`. |
| `graph` | How the imported resources fit together, to draw them: a node for each imported resource, with its type, ID, account and region as the inventory has them, and for each module call, with its registry source and version (`local` or `other` for a module from a path or a URL), each resource in a module naming it as `parent`; and an edge (`from`, `to`, the nodes' indexes) for each reference between them that the generated configuration writes, directly or through a local or a data source. Instances of `for_each` and `count` resources refer to the instance with the same key, as `each.key` and `count.index` make them. See [The graph](#the-graph). |
| `drift-summary` | What `infraharvest drift` found, counted: for each root, and each environment of a root [environments share](configuration.md), whether it was checked, how many changes in the cloud there are, by what became of them (`written`, `kept`, `rejected_by_plan`, `rejected_by_check`, `manual`, `secret`, `not_assessable` or `absorbed`, as in [Drift](drift.md#what-it-reports)) and by resource type, and the addresses of the resources that changed. See [The drift summary](#the-drift-summary). |
| `ocsf-inventory` | The inventory's resources as [OCSF](https://ocsf.io) 1.9.0 events of the class Cloud Resources Inventory Info (`class_uid` 5023), one event for each resource, each with its cloud, account and region, its type, ID, name and tags, who manages it, and, for a resource the import took over, the address it has in the configuration, the module call it is in and the path of its root. See [The OCSF inventory](#the-ocsf-inventory). |

No document has an attribute, a value or a message, and only the drift summary and the OCSF inventory have resource addresses, and the OCSF inventory the paths of the roots too: every field is copied by name, and nothing else of the report or the saved inventory is read. The inventory takes its names and tags from what `discover` saved under `.infraharvest/`; without it, the resources are those the report names, without names or tags. Before it is written, each document is checked against its schema, and an export that doesn't match writes nothing.

## What it leaves out

- **Tags that look like secrets.** A tag is dropped when the detectors the verification gate uses flag its key or its value, such as `db_password`, or a GitHub token. The inventory lists the dropped keys (`redaction.tag_keys_dropped`), unless the key is itself a credential.
- **Tags you choose.** `--drop-tag <key>` drops a tag key (repeat it, or separate keys with commas), and `--drop-tags` drops all tags.
- **IDs and names, if you hash them.** `--hash-ids` replaces each ID and name, and the addresses and root paths of the drift summary and the addresses, module addresses and root paths of the OCSF inventory, with its HMAC-SHA256 in hexadecimal. The key is yours: it comes from the environment variable `INFRAHARVEST_EXPORT_KEY`, or from a file with `--hash-key-file`, and must have at least 16 bytes. Nothing writes it down. The same key gives the same hash for a resource in every export, so the control plane can follow it over time; without the key, nobody can check a guess. `--hash-ids` without a key is an error. With it, tags don't give the ID or name away either: the `Name` tag's value, and a value that is or holds the resource's ID or name (one of 4 characters or more), such as an ARN, are hashed with the same key, and a tag whose key holds one, such as `kubernetes.io/cluster/<name>`, is dropped without being listed. Other tags can still name things, such as `aws:cloudformation:stack-name`: drop them with `--drop-tag`.
- **Addresses, if you drop them.** `--drop-addresses` leaves the resource addresses and the paths of roots out of the documents that have them, which are the drift summary and the OCSF inventory, which also loses its module addresses; the counts, and the OCSF inventory's resources, stay. It is for a control plane that needs to know how much drifted, or what exists, not how the code names it. With `--hash-ids`, the addresses are hashed with the same key instead, so an address in one export is the same hash in the next. A hashed address doesn't join the inventory's hashed ID: the two hash different text.

```sh
export INFRAHARVEST_EXPORT_KEY="$(openssl rand -hex 32)"   # keep it: another key hashes differently
infraharvest export infrastructure --hash-ids --drop-tag owner-email --out-file inventory.json
infraharvest export infrastructure --schema drift-summary --drop-addresses --out-file drift-summary.json
infraharvest export infrastructure --schema ocsf-inventory --hash-ids --out-file ocsf-inventory.json
```

Resource types, accounts, regions and scopes, and the counts, stay readable: they are what the control plane groups by. Account IDs are not hashed.

## Notes

- The export describes the last import in the directory. It adds the resources `discover` listed that the report doesn't name, as unmanaged.
- **Times.** The report holds no timestamps, so that its bytes, and the `run_id` made from them, name the run. `--started-at` and `--finished-at` (RFC 3339, such as `2026-11-03T09:30:00Z`; another offset is converted to UTC) say when the import ran: the run document gets them as `started_at` and `finished_at`, and `--finished-at` is also the inventory's `generated_at`. Without them `started_at` and `finished_at` are left out and `generated_at` is the time of the export, so exporting the same run again moves it. `--finished-at` may not be before `--started-at`. The [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md#sending-a-run-to-a-control-plane) records them around the import and passes them on.
- A resource whose ID looks like a credential stops the export, and so does an address, or the path of a root, in the drift summary and the OCSF inventory, unless `--drop-addresses` leaves them out.

## The run

The run document describes the last run of a command that leaves a report in the output directory: `import`, which also covers an incremental import, writes `report.json`, and `drift` writes `drift.json`. `discover` and `verify` leave no report, so they have no run document. The export reads the command from the directory: with only `drift.json` it describes the drift run, else the import. A directory that has both, as when you import and check for drift later, is described as the import, as it always was; `--command drift` describes the drift run instead. `--command import` without a `report.json`, or `--command drift` without a `drift.json`, is an error.

A drift run is `ok` when every root was checked, `partial` when some couldn't be, with the failure code `roots_not_checked`, and `failed` when none could. Drift found in the cloud isn't a failure of the run. It has no exit code, as that depends on `--detailed-exitcode`, and no versions or counts: those are an import's. Its `run_id` is made from `drift.json`, as an import's is from `report.json`, so it differs from the import's.

`--request-id <id>` puts a `request_id` into the run document: the ID a control plane gave to ask for the run, so that it can match the run to its request. It is 1 to 128 letters, digits, `.`, `_`, `:` and `-`. No other document has it.

## The drift summary

`drift-summary` is made from `_reports/drift.json`, which `infraharvest drift` writes, so it needs a directory that has been checked for drift; it doesn't read the import's report. Drift writes values into `drift.json`, and the export never reads them: it takes each change's root, environment, address, resource type and outcome, and nothing else, so no attribute and no value, not even the name of the attribute, reaches the document. Its `run_id` is made from `drift.json`, as the run document of a drift run's is, so the two match. `generated_at` is the time of the export, or `--finished-at`.

Each root is listed with the environment it was checked as, if it is one [environments share](configuration.md), and `checked`: `false` for a root drift couldn't check, which has no changes counted, and whose error is not in the document. Each root has its changes counted by outcome and by resource type, with `outcomes` having every outcome of [Drift](drift.md#what-it-reports), so a control plane can say how many changes the next apply reverts (`kept`), how many need a hand edit (`manual`, `rejected_by_plan` and `rejected_by_check`) and how many are secrets. A change of tags that several resources get from one place, such as `local.tags`, has no resource type: it is counted under the type `shared_tags`, and the resources it covers are in the root's `addresses`, not the place that sets the tags. `drift_found` is the report's: whether a change is left that the next apply reverts or that can't be assessed.

`addresses` lists the resources that changed in each root, once each, sorted, as Terraform names them, such as `module.db.aws_db_instance.main` or `aws_s3_bucket.named["logs"]`. An address can hold the key of a `for_each`, which can be a name: hash the addresses with `--hash-ids`, which is the same HMAC under the same key as the inventory's, or leave them out with `--drop-addresses`, which wins when both are given. `redaction.addresses` says which was done: `plain`, `hashed` or `dropped`. An address that looks like a credential stops the export. A root's `root` is its path in the output directory: `--hash-ids` hashes it with the addresses, and `--drop-addresses` leaves it out, so the roots are told apart only by their environment and order. Environments aren't hashed, and a path or environment that looks like a credential stops the export.

## The OCSF inventory

`ocsf-inventory` has the resources of the inventory, the same ones in the same order, for a consumer that reads [OCSF](https://ocsf.io) (the Open Cybersecurity Schema Framework), such as a security data lake. It is one JSON document, `{"schema": "infraharvest/ocsf-inventory/v1", "run_id": ..., "ocsf_version": "1.9.0", "redaction": {...}, "events": [...]}`, with the same `run_id` as the others, and its schema ([`ocsf-inventory-v1.schema.json`](https://github.com/IgnatG/infraharvest/blob/main/schemas/ocsf-inventory-v1.schema.json)) is closed, like theirs. It describes the subset of OCSF that infraharvest writes, not the whole schema.

`redaction` says what was left out or hashed, as the inventory's does, so that a receiver can tell a hashed ID from a real one: `tag_keys_dropped` (the keys of the tags that were dropped, unless the key is itself a credential), `all_tags_dropped` (present, and `true`, with `--drop-tags`), `ids_hashed` and `hash_alg` (`hmac-sha256`), which cover the IDs, names, addresses, module addresses and paths alike, and `addresses_dropped`, `true` with `--drop-addresses`, which leaves the addresses, module addresses and paths out.

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

The `iac_source` profile is infraharvest's own OCSF extension, `iac` (`uid` 1301), under [`schemas/ocsf/iac/`](https://github.com/IgnatG/infraharvest/tree/main/schemas/ocsf/iac), in the layout OCSF's extensions have, so that `ocsf-schema-compiler path/to/ocsf-schema -e schemas/ocsf/iac` compiles it with the core schema. It adds the object `iac_source`, as the attribute `iac`, to the object `resource_details`, with:

| Field | What it is | Filled by export |
|---|---|---|
| `tool` | `terraform` or `opentofu` | Yes, from the engine the import ran. |
| `address` | The resource's address in the configuration, such as `aws_s3_bucket.logs` or `module.db.aws_db_instance.main` | Yes. |
| `module_address` | The module call the resource is in, such as `module.db`, or `module.a["x"].module.b` for nested calls | Yes, for a resource in a module. |
| `repo_url` | The repository that holds the configuration | If you pass `--repo-url`, or, for this document only, `GITHUB_SERVER_URL` and `GITHUB_REPOSITORY` are set, as in GitHub Actions. An `http` or `https` URL without credentials, a query or a fragment; a trailing `/` or `.git` is dropped. |
| `commit_sha` | The commit the configuration is from, 40 or 64 lowercase hexadecimal digits | If you pass `--commit-sha`, or `GITHUB_SHA` is set. |
| `path` | The directory of the resource's root, relative to the output directory, such as `aws/123456789012/eu-west-2` | Yes. |
| `workspace` | The workspace whose state records the resource | No: the extension has it, the export doesn't know it. |
| `state_key` | The key of that state in its backend | No, likewise. |

Only a resource the import took over has an `iac` source; the export doesn't know where a resource it found managed by a state, a tag or CloudFormation is written, so those have none, and neither does a resource left out. `repo_url` and `commit_sha` are written to each of those that has one, and are not hashed: you name them. `--drop-addresses` leaves `address`, `module_address` and `path` out, and `--hash-ids` hashes them as it does the drift summary's addresses, so an address is the same hash in both documents, and the OCSF inventory's from one export to the next. An address, or a path, that looks like a credential, as a key of a `for_each` can, stops the export.

The extension isn't in the [OCSF Extensions Registry](https://github.com/ocsf/ocsf-schema/blob/main/extensions.md): its `uid` is outside the registered range, 1 to 999, so that it can't collide with a registered one. It is meant to be registered if others adopt it.

## The graph

The graph is drawn from the configuration of each root, so it shows what the code says: a subnet's `vpc_id = aws_vpc.main.id` is an edge from the subnet to the VPC. It has the resources the import took over, not the ones it left out or excluded. References between roots go through variables (see [Output](output.md)), and references inside a module's own code are the module's, so neither is an edge; the roots of [environments](configuration.md) merged into a root they share have their nodes, but no edges yet: the merged root names their resources at other addresses. The same output gives the same graph, nodes and edges in the same order, so two graphs can be compared.

`--hash-ids` hashes a node's ID with the same key as the inventory's, so the graph and the inventory join on type and ID. The graph has no tags, so `--drop-tag` and `--drop-tags` don't change it.

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

The Action doesn't upload `inventory/v1`: it stays a document `infraharvest export` writes. The OCSF inventory has the redaction flags the Action is given (tags dropped, IDs and addresses hashed, addresses dropped), and says which in its `redaction` block, and the repository and the commit from the job's `GITHUB_SERVER_URL`, `GITHUB_REPOSITORY` and `GITHUB_SHA`.

- **Session.** First, the Action sends the job's GitHub Actions OIDC token, requested for the audience in `control-plane-audience` (default `infraharvest-api`), in the header `X-GitHub-OIDC-Token` of `POST <base>/api/v1/session`, with an empty body. Verify it against GitHub's keys (`https://token.actions.githubusercontent.com/.well-known/jwks`): the issuer, the audience, the expiry, and the `repository`, `ref` and `sub` claims that say which workflow sent it. A `200` answers `{"token": "<run token>", "expires_at": "<RFC 3339 time>"}`. The OIDC token goes to this endpoint only; the uploads carry the run token. The Action masks both and doesn't print them.
- **Order.** The documents are sent one after another after the session, and the next is not sent if one fails: an import's OCSF inventory, run, gate and graph, in that order; a drift check's run document (when no import ran) and then the drift summary, last of all. The documents of an import share a `run_id`; the drift summary has the drift check's own, which its run document has too. Sending a document again (after a retry) must give the same result: key it by `run_id` and the endpoint.
- **Headers.** `Authorization: Bearer <run token>`, from the session; `Content-Type: application/json`; `Content-Encoding: gzip`; and `Idempotency-Key: <run_id>:<document>`, such as `6f1c9d3e-0a52-5b8e-9c47-2d1f3a5b7c90:ocsf-inventory`, the same for each send of a document of a run.
- **Body.** The document, as `infraharvest export` wrote it, gzip-compressed, so decompress it before reading it as JSON; validated against its schema ([`schemas/`](https://github.com/IgnatG/infraharvest/tree/main/schemas)). A server should check it again.
- **Response.** Any `2xx` means the document was accepted; the body is ignored (`201` or `204` are fine). Any `4xx` other than `429` fails the run without a retry, such as `401` and `403` (token refused), `400` and `422` (document refused), and `402` (the organisation's plan refused it, such as for being over its cloud-account limit). The same goes for the session request. Say why in a JSON body, `{"error": "..."}`: the Action prints it, or the start of the body if it isn't JSON. `429` and `5xx` are retried up to four times in all, after 2, 4 and 8 seconds, so a server that is overloaded can say so. Redirects are not followed.
- **Transport.** The Action accepts `https://` URLs only, and `http://` for `localhost`, `127.0.0.1` and `[::1]`, for a server run in the same job.
