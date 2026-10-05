### Use with Honeycomb.io

#### Example

```sh
export HONEYCOMB_API_KEY=MYAPIKEY
infraharvest import honeycombio --all --resources=board,trigger
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover honeycombio` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

#### List of supported Honeycomb resources

* `board`
  * `honeycombio_board`
  * `honeycombio_query`
  * `honeycombio_query_annotation`
* `derived_column`
  * `honeycombio_derived_column`
* `trigger`
  * `honeycombio_query`
  * `honeycombio_trigger`
* `slo`
  * `honeycombio_burn_alert`
  * `honeycombio_derived_column`
  * `honeycombio_slo`

#### A note about Environment-wide assets

If no datasets are specified via the `--datasets` argument, and the API key is *not* for a Honeycomb Classic environment, the `__all__` dataset for Environment-wide assets (e.g. derived columns, boards) will be appended to the dataset list.

If you wish to import a specific list of datasets *including* environment-wide assets (e.g. derived columns, boards) you must add `__all__` to the list of provided datasets.

```sh
export HONEYCOMB_API_KEY=MYAPIKEY
infraharvest import honeycombio --all --resources=derived_column,board --datasets=__all__,my.service
```
