# Updating Chainguard Images in Dockerfiles

Example Renovate configurations that update Chainguard image references in a
`Dockerfile`.

## Without Cooldown

If you aren't interested in cooldown, then you only need to use the custom
changelog URL functionality of the datasource.

Renovate's built-in `dockerfile` manager already updates both the tag and
digest on `FROM` references, so no custom manager is needed.

To use this example yourself, replace every instance of `cgr.dev/my-org` with
your own Chainguard organization name or internal mirror/proxy.

```jsonc
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "packageRules": [
    {
      "matchDatasources": ["docker"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/"],
      "changelogUrl": "http://<datasource-host>/repo/{{{replace \"cgr.dev/my-org/\" \"\" packageName}}}/diff/{{#if currentDigest}}{{currentDigest}}{{else}}{{currentValue}}{{/if}}/{{#if newDigest}}{{newDigest}}{{else}}{{newValue}}{{/if}}"
    }
  ]
}
```

## With Cooldown

Use the custom datasource to take advantage of the cooldown functionality.

To use this example:

- Replace `<datasource-host>` with the hostname of the datasource running
  in your environment.
- Adjust the cooldown window by changing the `cooldown=168h` query parameter.
- Replace every instance of `cgr.dev/my-org` with your own Chainguard organization
  name or internal mirror/proxy.

```jsonc
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "customDatasources": {
    "chainguard-repo": {
      "defaultRegistryUrlTemplate": "http://<datasource-host>/v1/repo/{{packageName}}/releases?cooldown=168h",
      "format": "json"
    }
  },
  "packageRules": [
    {
      "matchManagers": ["dockerfile"],
      "matchDatasources": ["docker"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/"],
      "overrideDatasource": "custom.chainguard-repo",
      "overridePackageName": "{{{replace \"cgr.dev/my-org/\" \"\" packageName}}}"
    },
    {
      "matchDatasources": ["custom.chainguard-repo"],
      "versioning": "docker",
      "changelogUrl": "http://<datasource-host>/repo/{{packageName}}/diff/{{#if currentDigest}}{{currentDigest}}{{else}}{{currentValue}}{{/if}}/{{#if newDigest}}{{newDigest}}{{else}}{{newValue}}{{/if}}"
    }
  ]
}
```
