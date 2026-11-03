# Export

`infraharvest export` writes the metadata of an import into a file you can hand to a hosted control plane, or keep. It is opt-in and local: it reads the output directory of an import, uploads nothing and makes no network calls. What you do with the file is up to you.

```sh
infraharvest export infrastructure --out-file inventory.json
infraharvest export infrastructure --schema run --out-file run.json
infraharvest export infrastructure --schema gate --out-file gate.json
```

Without `--out-file` the document goes to standard output. The directory defaults to `infrastructure`, as for [`report`](reference/cli.md#infraharvest-report).

## The documents

Each document has a JSON Schema (2020-12, no fields beyond the ones it lists), and the three share one `run_id`, made from the report, so they can be matched. `infraharvest export --schema inventory --print-schema` prints a schema; the files are under [`schemas/`](https://github.com/IgnatG/infraharvest/tree/main/schemas) in the repository.

| `--schema` | What it has |
|---|---|
| `inventory` | The cloud, the scopes (provider, account and region), and for each resource its type, ID, account, region, name, tags and who manages it: `managed` (by a Terraform state, a root of the output, or Terraform's tags), `other_iac` (CloudFormation or another tool) or `unmanaged`, with whether a state, a tag or nothing says so. The counts of the report, in all, by type and by scope. What the export left out (see below). |
| `run` | The command, exit code and outcome (`ok`, `partial`, `failed` or `interrupted`), when it started and finished if you pass `--started-at` and `--finished-at`, the versions of infraharvest and of Terraform or OpenTofu, the counts, and failure codes from a fixed list. |
| `gate` | For each check of the [verification gate](output.md#verification-gate) (G1 to G8): whether it passed or was skipped, with reason codes from a fixed list, such as `secrets_found` or `tool_missing`. |

It has no attributes, no values, no resource addresses, no file paths and no messages: every field is copied by name, and nothing else of the report or the saved inventory is read. The inventory takes its names and tags from what `discover` saved under `.infraharvest/`; without it, the resources are those the report names, without names or tags. Before it is written, each document is checked against its schema, and an export that doesn't match writes nothing.

## What it leaves out

- **Tags that look like secrets.** A tag is dropped when the detectors the verification gate uses flag its key or its value, such as `db_password`, or a GitHub token. The inventory lists the dropped keys (`redaction.tag_keys_dropped`), unless the key is itself a credential.
- **Tags you choose.** `--drop-tag <key>` drops a tag key (repeat it, or separate keys with commas), and `--drop-tags` drops all tags.
- **IDs and names, if you hash them.** `--hash-ids` replaces each ID and name with its HMAC-SHA256 in hexadecimal. The key is yours: it comes from the environment variable `INFRAHARVEST_EXPORT_KEY`, or from a file with `--hash-key-file`, and must have at least 16 bytes. Nothing writes it down. The same key gives the same hash for a resource in every export, so the control plane can follow it over time; without the key, nobody can check a guess. `--hash-ids` without a key is an error.

```sh
export INFRAHARVEST_EXPORT_KEY="$(openssl rand -hex 32)"   # keep it: another key hashes differently
infraharvest export infrastructure --hash-ids --drop-tag owner-email --out-file inventory.json
```

Resource types, accounts, regions and scopes, and the counts, stay readable: they are what the control plane groups by. Account IDs are not hashed.

## Notes

- The export describes the last import in the directory. It adds the resources `discover` listed that the report doesn't name, as unmanaged.
- **Times.** The report holds no timestamps, so that its bytes, and the `run_id` made from them, name the run. `--started-at` and `--finished-at` (RFC 3339, such as `2026-11-03T09:30:00Z`; another offset is converted to UTC) say when the import ran: the run document gets them as `started_at` and `finished_at`, and `--finished-at` is also the inventory's `generated_at`. Without them `started_at` and `finished_at` are left out and `generated_at` is the time of the export, so exporting the same run again moves it. `--finished-at` may not be before `--started-at`. The [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md#sending-a-run-to-a-control-plane) records them around the import and passes them on.
- A resource whose ID looks like a credential stops the export.

## Control plane API

The [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md#sending-a-run-to-a-control-plane) sends the documents to a control plane when `control-plane-url` is set. `infraharvest export` itself never does. A server that implements the following can receive them; `<base>` is the URL as given, without a trailing slash, and may have a path.

| Request | Body |
|---|---|
| `POST <base>/api/v1/ingest/inventory` | an `inventory/v1` document |
| `POST <base>/api/v1/ingest/run` | a `run/v1` document |
| `POST <base>/api/v1/ingest/gate` | a `gate/v1` document |

- **Order.** The three are sent one after another, in that order, and the next is not sent if one fails. They share a `run_id`, and sending a document again (after a retry) must give the same result: key it by `run_id` and the endpoint.
- **Headers.** `Content-Type: application/json`, and `Authorization: Bearer <token>`, a GitHub Actions OIDC token with the audience `infraharvest-api`. Verify it against GitHub's keys (`https://token.actions.githubusercontent.com/.well-known/jwks`): the issuer, the audience, the expiry, and the `repository`, `ref` and `sub` claims that say which workflow sent it. The token lives for minutes; the Action requests one per upload.
- **Body.** The document, as `infraharvest export` wrote it, validated against its schema ([`schemas/`](https://github.com/IgnatG/infraharvest/tree/main/schemas)). A server should check it again.
- **Response.** Any `2xx` means the document was accepted; the body is ignored (`201` or `204` are fine). `401` and `403` (token refused), `400` and `422` (document refused) fail the run without a retry. `429` and `5xx` are retried up to four times in all, after 2, 4 and 8 seconds, so a server that is overloaded can say so. Redirects are not followed.
- **Transport.** The Action accepts `https://` URLs only, and `http://` for `localhost`, `127.0.0.1` and `[::1]`, for a server run in the same job.
