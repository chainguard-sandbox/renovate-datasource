# Updating Chainguard Images in Dockerfiles

Example Renovate configuration that updates Chainguard image references in
a `Dockerfile` using the custom datasource.

Renovate's built-in `dockerfile` manager already extracts image references,
so no custom manager is needed — we just redirect its lookups through the
custom datasource so Renovate can honour the `minimumReleaseAge` window.

To use this example:

- Replace `<datasource-host>` with the hostname of the datasource running
  in your environment.
- Adjust the window by changing the `minimumReleaseAge=168h` query
  parameter, or drop it entirely to disable per-request overrides (the
  server-wide `--min-release-age` flag still applies, if set).
- Replace every instance of `cgr.dev/my-org` with your own Chainguard
  organization name or internal mirror/proxy.

```jsonc
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "customDatasources": {
    "chainguard-repo": {
      "defaultRegistryUrlTemplate": "http://<datasource-host>/v1/repo/{{packageName}}/releases?minimumReleaseAge=168h",
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
      "excludePackagePatterns": ["^(charts|iamguarded-charts)/"],
      "versioning": "docker"
    }
  ]
}
```
