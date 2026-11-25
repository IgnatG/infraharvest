# Go packages

infraharvest's code is under `internal/`, which no other Go module can import, with one exception: three small packages under `pkg/` that tools reading or reporting infrastructure share with it. They are the public surface of the module `github.com/IgnatG/infraharvest`, and the only Go API it promises.

| Package | What it offers |
|---|---|
| [`pkg/tfstate`](#pkgtfstate) | Reads Terraform and OpenTofu state from files, Amazon S3, Google Cloud Storage, Azure Blob Storage and HCP Terraform, without running Terraform. |
| [`pkg/typemap`](#pkgtypemap) | Maps cloud inventory types and ARNs to Terraform resource types, and a type to its other names and its child types. |
| [`pkg/secretscan`](#pkgsecretscan) | Tells secrets from other text, filters tags and hashes IDs as `infraharvest export` does. |

## What is promised

- **Semver-tracked.** These packages follow the module's version. Until the first tagged release everything under `pkg/` is unstable; from it, a change that breaks an importer waits for a new major version. The module's path changes once, with the move to its organisation.
- **Additive.** The fields of the types, the forms of location `tfstate.Read` takes and the answers of the detectors only grow. A detector may learn a new credential format and never forgets one. A hash never changes for the same key and value.
- **Small.** Each package has a doc comment that says what it promises, and every exported name is documented. Nothing in a signature is an internal type. What the catalogue knows about generating configuration (the arguments to leave out, the defaults) and the YAML it is written in stay internal.
- **Light.** `pkg/` never links the engine, Terraform's execution libraries (`terraform-exec`, `hc-install`) or the providers' listers. Two checks enforce it: depguard rules in `.golangci.json` for the imports in the source, and `pkg/boundary_test.go` for what a package links, with a budget of packages of infraharvest's own for each. Adding a package to `pkg/` means adding its budget there.
- **The commands behave as before.** The commands read state, tag filters and types through these packages, so that what they find is what an importer finds; a change here is a change to them.
- **Licence.** Like the rest of infraharvest, AGPL-3.0-only. A product that is not licensed under the AGPL needs the commercial licence (see the [README](https://github.com/IgnatG/infraharvest/blob/main/README.md#licence)) to import them.

## pkg/tfstate

`tfstate.Read(ctx, location, stores)` returns a `Document` for each state a location names: a state file or a directory of them, `s3://bucket/prefix`, `gs://bucket/prefix`, `https://<account>.blob.core.windows.net/container/prefix` or `tfc://organization/workspace` (`prefix*` for several), as `--managed-state` takes them. A `Document` has the state's serial, lineage and Terraform version, its `Instances` (mode, module, type, name, address, provider, ID, ARN, the ID of its resource identity and the addresses it depends on), the names of its outputs with whether each is sensitive, and the places its `terraform_remote_state` data sources read other states from.

State holds secrets, so the reader keeps only that. Attribute values and output values are dropped while the state is decoded, one resource at a time, so that a large state doesn't take its size in memory, and a remote state keeps where it reads from and none of its credentials. State format version 4 is read; version 3 is refused with a hint to upgrade it.

A program passes the stores it wants in `tfstate.Stores`, each from its own package: `stores/s3`, `stores/gcs`, `stores/azblob` and `stores/hcp`, each with a `New`. A build links the SDKs of the clouds it names and no others; the HCP store is plain HTTP and links none.

## pkg/typemap

`typemap.New()` returns a `Resolver` over the built-in catalogue, which is embedded: nothing is read from disk or the network. `FromInventoryType(cloud, assetType)` gives the Terraform types of a Cloud Asset Inventory type or a CloudFormation type, and `FromInventoryName` tells apart the types that one inventory type covers by the resource's name. `FromARN(arn)` gives the AWS types an ARN names. `Canonical(type)` gives the type its other names are matched as (`aws_alb` is `aws_lb`), `Children(type)` the separate resources that hold parts of its configuration, and `Providers()` the providers covered. A lookup that finds nothing means unknown, not unmanaged: coverage grows with each release, and Azure inventory types aren't mapped yet.

## pkg/secretscan

`IsSecretName(key)`, `CredentialFormat(value)` and `LooksSecret(key, value)` are the detectors the secrets check and `infraharvest export` share. `NewTagFilter` is the filter `export` applies to tags (`--drop-tag`, `--drop-tags`, `--hash-ids`), `Hash(key, s)` the HMAC-SHA256 of `--hash-ids`, and `ReadKey` reads its key from `INFRAHARVEST_EXPORT_KEY` or a file. The same key gives the same hash in infraharvest and in a tool that imports the package, so the two can be joined. The plan scanner and everything that runs Terraform stay in the engine.

The answers are tested against what the detectors gave before they were shared (`internal/secretdetect/parity`), byte for byte, from each of the packages that offers them.
