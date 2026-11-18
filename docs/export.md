# Export

`infraharvest export` writes the metadata of an import into a file you can hand to a hosted control plane, or keep. It is opt-in and local: it reads the output directory of an import, uploads nothing and makes no network calls. What you do with the file is up to you.

```sh
infraharvest export infrastructure --out-file inventory.json
infraharvest export infrastructure --schema run --out-file run.json
infraharvest export infrastructure --schema gate --out-file gate.json
infraharvest export infrastructure --schema graph --out-file graph.json
```

Without `--out-file` the document goes to standard output. The directory defaults to `infrastructure`, as for [`report`](reference/cli.md#infraharvest-report).

## The documents

Each document has a JSON Schema (2020-12, no fields beyond the ones it lists), and they all share one `run_id`, made from the report, so they can be matched. `infraharvest export --schema inventory --print-schema` prints a schema; the files are under [`schemas/`](https://github.com/IgnatG/infraharvest/tree/main/schemas) in the repository.

| `--schema` | What it has |
|---|---|
| `inventory` | The cloud, the scopes (provider, account and region), and for each resource its type, ID, account, region, name, tags and who manages it: `managed` (by a Terraform state, a root of the output, or Terraform's tags), `other_iac` (CloudFormation or another tool) or `unmanaged`, with whether a state, a tag or nothing says so. The counts of the report, in all, by type and by scope. What the export left out (see below). |
| `run` | The command, exit code and outcome (`ok`, `partial`, `failed` or `interrupted`), when it started and finished if you pass `--started-at` and `--finished-at`, the versions of infraharvest and of Terraform or OpenTofu, the counts, and failure codes from a fixed list. |
| `gate` | For each check of the [verification gate](output.md#verification-gate) (G1 to G8): whether it passed or was skipped, with reason codes from a fixed list, such as `secrets_found` or `tool_missing`. |
| `graph` | How the imported resources fit together, to draw them: a node for each imported resource, with its type, ID, account and region as the inventory has them, and for each module call, with its registry source and version (`local` or `other` for a module from a path or a URL), each resource in a module naming it as `parent`; and an edge (`from`, `to`, the nodes' indexes) for each reference between them that the generated configuration writes, directly or through a local or a data source. Instances of `for_each` and `count` resources refer to the instance with the same key, as `each.key` and `count.index` make them. See [The graph](#the-graph). |

It has no attributes, no values, no resource addresses, no file paths and no messages: every field is copied by name, and nothing else of the report or the saved inventory is read. The inventory takes its names and tags from what `discover` saved under `.infraharvest/`; without it, the resources are those the report names, without names or tags. Before it is written, each document is checked against its schema, and an export that doesn't match writes nothing.

## What it leaves out

- **Tags that look like secrets.** A tag is dropped when the detectors the verification gate uses flag its key or its value, such as `db_password`, or a GitHub token. The inventory lists the dropped keys (`redaction.tag_keys_dropped`), unless the key is itself a credential.
- **Tags you choose.** `--drop-tag <key>` drops a tag key (repeat it, or separate keys with commas), and `--drop-tags` drops all tags.
- **IDs and names, if you hash them.** `--hash-ids` replaces each ID and name with its HMAC-SHA256 in hexadecimal. The key is yours: it comes from the environment variable `INFRAHARVEST_EXPORT_KEY`, or from a file with `--hash-key-file`, and must have at least 16 bytes. Nothing writes it down. The same key gives the same hash for a resource in every export, so the control plane can follow it over time; without the key, nobody can check a guess. `--hash-ids` without a key is an error. With it, tags don't give the ID or name away either: the `Name` tag's value, and a value that is or holds the resource's ID or name (one of 4 characters or more), such as an ARN, are hashed with the same key, and a tag whose key holds one, such as `kubernetes.io/cluster/<name>`, is dropped without being listed. Other tags can still name things, such as `aws:cloudformation:stack-name`: drop them with `--drop-tag`.

```sh
export INFRAHARVEST_EXPORT_KEY="$(openssl rand -hex 32)"   # keep it: another key hashes differently
infraharvest export infrastructure --hash-ids --drop-tag owner-email --out-file inventory.json
```

Resource types, accounts, regions and scopes, and the counts, stay readable: they are what the control plane groups by. Account IDs are not hashed.

## Notes

- The export describes the last import in the directory. It adds the resources `discover` listed that the report doesn't name, as unmanaged.
- **Times.** The report holds no timestamps, so that its bytes, and the `run_id` made from them, name the run. `--started-at` and `--finished-at` (RFC 3339, such as `2026-11-03T09:30:00Z`; another offset is converted to UTC) say when the import ran: the run document gets them as `started_at` and `finished_at`, and `--finished-at` is also the inventory's `generated_at`. Without them `started_at` and `finished_at` are left out and `generated_at` is the time of the export, so exporting the same run again moves it. `--finished-at` may not be before `--started-at`. The [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md#sending-a-run-to-a-control-plane) records them around the import and passes them on.
- A resource whose ID looks like a credential stops the export.

## The graph

The graph is drawn from the configuration of each root, so it shows what the code says: a subnet's `vpc_id = aws_vpc.main.id` is an edge from the subnet to the VPC. It has the resources the import took over, not the ones it left out or excluded. References between roots go through variables (see [Output](output.md)), and references inside a module's own code are the module's, so neither is an edge; the roots of [environments](configuration.md) merged into a root they share have their nodes, but no edges yet: the merged root names their resources at other addresses. The same output gives the same graph, nodes and edges in the same order, so two graphs can be compared.

`--hash-ids` hashes a node's ID with the same key as the inventory's, so the graph and the inventory join on type and ID. The graph has no tags, so `--drop-tag` and `--drop-tags` don't change it.

## Control plane API

The [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md#sending-a-run-to-a-control-plane) sends the inventory, run, gate and graph documents to a control plane when `control-plane-url` is set. `infraharvest export` itself never does. A server that implements the following can receive them; `<base>` is the URL as given, without a trailing slash, and may have a path.

| Request | Body |
|---|---|
| `POST <base>/api/v1/session` | empty; opens a session (see below) |
| `POST <base>/api/v1/ingest/inventory` | an `inventory/v1` document |
| `POST <base>/api/v1/ingest/run` | a `run/v1` document |
| `POST <base>/api/v1/ingest/gate` | a `gate/v1` document |
| `POST <base>/api/v1/ingest/graph` | a `graph/v1` document |

- **Session.** First, the Action sends the job's GitHub Actions OIDC token, requested for the audience in `control-plane-audience` (default `infraharvest-api`), in the header `X-GitHub-OIDC-Token` of `POST <base>/api/v1/session`, with an empty body. Verify it against GitHub's keys (`https://token.actions.githubusercontent.com/.well-known/jwks`): the issuer, the audience, the expiry, and the `repository`, `ref` and `sub` claims that say which workflow sent it. A `200` answers `{"token": "<run token>", "expires_at": "<RFC 3339 time>"}`. The OIDC token goes to this endpoint only; the uploads carry the run token. The Action masks both and doesn't print them.
- **Order.** The four are sent one after another, in that order, after the session, and the next is not sent if one fails. They share a `run_id`, and sending a document again (after a retry) must give the same result: key it by `run_id` and the endpoint.
- **Headers.** `Authorization: Bearer <run token>`, from the session; `Content-Type: application/json`; `Content-Encoding: gzip`; and `Idempotency-Key: <run_id>:<document>`, such as `6f1c9d3e-0a52-5b8e-9c47-2d1f3a5b7c90:inventory`, the same for each send of a document of a run.
- **Body.** The document, as `infraharvest export` wrote it, gzip-compressed, so decompress it before reading it as JSON; validated against its schema ([`schemas/`](https://github.com/IgnatG/infraharvest/tree/main/schemas)). A server should check it again.
- **Response.** Any `2xx` means the document was accepted; the body is ignored (`201` or `204` are fine). Any `4xx` other than `429` fails the run without a retry, such as `401` and `403` (token refused), `400` and `422` (document refused), and `402` (the organisation's plan refused it, such as for being over its cloud-account limit). The same goes for the session request. Say why in a JSON body, `{"error": "..."}`: the Action prints it, or the start of the body if it isn't JSON. `429` and `5xx` are retried up to four times in all, after 2, 4 and 8 seconds, so a server that is overloaded can say so. Redirects are not followed.
- **Transport.** The Action accepts `https://` URLs only, and `http://` for `localhost`, `127.0.0.1` and `[::1]`, for a server run in the same job.
