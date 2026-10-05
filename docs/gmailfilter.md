### Use with GmailFilter

Support [Using Service Accounts](https://github.com/yamamoto-febc/terraform-provider-gmailfilter/blob/master/README.md#using-a-service-accountg-suite-users-only) or [Using Application Default Credentials](https://github.com/yamamoto-febc/terraform-provider-gmailfilter/blob/master/README.md#using-an-application-default-credential).

Example:

```
# Using Service Accounts
export GOOGLE_CREDENTIALS=/path/to/client_secret.json
export IMPERSONATED_USER_EMAIL="foobar@example.com"

# Using Application Default Credentials
gcloud auth application-default login \
  --client-id-file=client_secret.json \
  --scopes \
https://www.googleapis.com/auth/gmail.labels,\
https://www.googleapis.com/auth/gmail.settings.basic

infraharvest import gmailfilter --all --resources=filter,label
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover gmailfilter` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported GmailFilter resources:

*   `filter`
    * `gmailfilter_filter`
*   `label`
    * `gmailfilter_label`
