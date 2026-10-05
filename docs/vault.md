### Use with Vault

Example:

```sh
export VAULT_TOKEN=YOUR_VAULT_TOKEN     # or pass --token=YOUR_VAULT_TOKEN
export VAULT_ADDR=YOUR_VAULT_ADDRESS    # or pass --address=YOUR_VAULT_ADDRESS
infraharvest import vault --all --resources=aws_secret_backend_role
infraharvest import vault --all --resources=policy --filter=policy=id1:id2:id4
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover vault` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported Vault resources:

* `ad_secret_backend`
    * `ad_secret_backend`
* `ad_secret_backend_role`
    * `ad_secret_backend_role`
* `alicloud_auth_backend_role`
    * `alicloud_auth_backend_role`
* `approle_auth_backend_role`
    * `approle_auth_backend_role`
* `aws_auth_backend_role`
    * `aws_auth_backend_role`
* `aws_secret_backend`
    * `aws_secret_backend`
* `aws_secret_backend_role`
    * `aws_secret_backend_role`
* `azure_auth_backend_role`
    * `azure_auth_backend_role`
* `azure_secret_backend`
    * `azure_secret_backend`
* `azure_secret_backend_role`
    * `azure_secret_backend_role`
* `cert_auth_backend_role`
    * `cert_auth_backend_role`
* `consul_secret_backend`
    * `consul_secret_backend`
* `consul_secret_backend_role`
    * `consul_secret_backend_role`
* `database_secret_backend_role`
    * `database_secret_backend_role`
* `gcp_auth_backend`
    * `gcp_auth_backend`
* `gcp_auth_backend_role`
    * `gcp_auth_backend_role`
* `gcp_secret_backend`
    * `gcp_secret_backend`
* `generic_secret`
    * `generic_secret`
* `github_auth_backend`
    * `github_auth_backend`
* `jwt_auth_backend`
    * `jwt_auth_backend`
* `jwt_auth_backend_role`
    * `jwt_auth_backend_role`
* `kubernetes_auth_backend_role`
    * `kubernetes_auth_backend_role`
* `ldap_auth_backend`
    * `ldap_auth_backend`
* `ldap_auth_backend_group`
    * `ldap_auth_backend_group`
* `ldap_auth_backend_user`
    * `ldap_auth_backend_user`
* `nomad_secret_backend`
    * `nomad_secret_backend`
* `okta_auth_backend`
    * `okta_auth_backend`
* `okta_auth_backend_group`
    * `okta_auth_backend_group`
* `okta_auth_backend_user`
    * `okta_auth_backend_user`
* `pki_secret_backend`
    * `pki_secret_backend`
* `pki_secret_backend_role`
    * `pki_secret_backend_role`
* `policy`
    * `policy`
* `rabbitmq_secret_backend`
    * `rabbitmq_secret_backend`
* `rabbitmq_secret_backend_role`
    * `rabbitmq_secret_backend_role`
* `ssh_secret_backend_role`
    * `ssh_secret_backend_role`
* `terraform_cloud_secret_backend`
    * `terraform_cloud_secret_backend`
* `token_auth_backend_role`
    * `token_auth_backend_role`

[1]: ../README.md#filtering
