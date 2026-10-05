### Use with RabbitMQ

Example:

```sh
export RABBITMQ_SERVER_URL=http://foo.bar.localdomain:15672
export RABBITMQ_USERNAME=[RABBITMQ_USERNAME]
export RABBITMQ_PASSWORD=[RABBITMQ_PASSWORD]

infraharvest import rabbitmq --all --resources=vhosts,queues,exchanges
infraharvest import rabbitmq --all --resources=vhosts,queues,exchanges --filter=vhost=name1:name2:name3
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover rabbitmq` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

All RabbitMQ resources that are currently supported by the RabbitMQ provider, are also supported by this module. Here is the list of resources which are currently supported by RabbitMQ provider v.1.1.0:

*   `bindings`
    * `rabbitmq_binding`
*   `exchanges`
    * `rabbitmq_exchange`
*   `permissions`
    * `rabbitmq_permissions`
*   `policies`
    * `rabbitmq_policy`
*   `queues`
    * `rabbitmq_queue`
*   `users`
    * `rabbitmq_user`
*   `vhosts`
    * `rabbitmq_vhost`
