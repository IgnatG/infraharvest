# Quickstart

Bring a few resources under Terraform in four steps: list them, choose, import, and plan. infraharvest only reads the cloud; nothing changes until you apply, and then only the state.

You need infraharvest ([Install](install.md)), credentials that can read the resources ([Permissions](permissions.md)), and Terraform 1.5 or later or OpenTofu 1.6 or later. Without Terraform on `PATH`, infraharvest downloads the latest release and verifies it.

## 1. List what is there

`discover` lists a provider's resources into a selection file, without importing anything. Name the services to list with `--services`.

=== "AWS"

    ```sh
    export AWS_PROFILE=prod
    infraharvest discover aws --services=vpc,subnet,sg,s3 --regions=eu-west-2
    ```

=== "Azure"

    ```sh
    az login
    export ARM_SUBSCRIPTION_ID=<subscription ID>
    infraharvest discover azure --services=resource_graph --resource-group=<resource group>
    ```

=== "Google Cloud"

    ```sh
    gcloud auth application-default login
    infraharvest discover google --services='*' --projects=<project ID> --regions=global,europe-west2
    ```

`infraharvest services <provider>` lists the services `--services=*` imports; each cloud's page also describes the services that are imported only when named.

## 2. Choose what to import

The selection file, `selection.yaml`, lists each resource with `include: true` or `false`. The default selection leaves out what the cloud creates on its own, such as default VPCs and security groups, and what another tool manages, such as CloudFormation stacks, with the reason. In a terminal, `discover` opens the picker on it (`--pick=false` doesn't); review it there or in a text editor, and open the picker again with:

```sh
infraharvest pick --selection selection.yaml
```

## 3. Import

`import`, with the same flags and the selection, writes one root per account and region:

```sh
infraharvest import aws --services=vpc,subnet,sg,s3 --regions=eu-west-2 --selection=selection.yaml
```

For each root, infraharvest writes an `import` block per resource and has Terraform generate their configuration. It then tidies the configuration: references instead of IDs, shared tags as a local, clusters of resources as calls of curated modules. It keeps a change only if a new plan shows no extra changes. Last, it runs the verification gate: format, validate, a plan with no changes, the output standard, scanners, secrets and determinism. `generated/report/report.md` summarises the run, and each root's `README.md` says what is left to do.

## 4. Plan and apply

```sh
cd infraharvest/aws/<account>/eu-west-2
terraform init
terraform plan    # imports every resource, changes nothing
terraform apply   # records the resources in the state, without changing them
```

Then delete `imports.tf`: the resources are in the state. To keep a remote state backend, set it in a [configuration file](configuration.md) before importing.

## Next

- [Usage](usage.md): selection rules, filters, incremental imports, roots per environment.
- [Output](output.md): what each root holds, and what the verification gate checks.
- [Modules](modules.md): the curated modules, and your own.
- Each cloud's page: [AWS](aws.md), [Azure](azure.md), [Google Cloud](google.md).
