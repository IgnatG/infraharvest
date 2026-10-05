### Use with DigitalOcean

Example:

```sh
export DIGITALOCEAN_TOKEN=[DIGITALOCEAN_TOKEN]
infraharvest import digitalocean --all --resources=project,droplet
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover digitalocean` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported DigitalOcean resources:

*   `cdn`
    * `digitalocean_cdn`
*   `certificate`
    * `digitalocean_certificate`
*   `database_cluster`
    * `digitalocean_database_cluster`
    * `digitalocean_database_connection_pool`
    * `digitalocean_database_db`
    * `digitalocean_database_replica`
    * `digitalocean_database_user`
*   `domain`
    * `digitalocean_domain`
    * `digitalocean_record`
*   `droplet`
    * `digitalocean_droplet`
*   `droplet_snapshot`
    * `digitalocean_droplet_snapshot`
*   `firewall`
    * `digitalocean_firewall`
*   `floating_ip`
    * `digitalocean_floating_ip`
*   `kubernetes_cluster`
    * `digitalocean_kubernetes_cluster`
    * `digitalocean_kubernetes_node_pool`
*   `loadbalancer`
    * `digitalocean_loadbalancer`
*   `project`
    * `digitalocean_project`
*   `ssh_key`
    * `digitalocean_ssh_key`
*   `tag`
    * `digitalocean_tag`
*   `volume`
    * `digitalocean_volume`
*   `volume_snapshot`
    * `digitalocean_volume_snapshot`
*   `vpc`
    * `digitalocean_vpc`
