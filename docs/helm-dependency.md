# Updating Chainguard Helm Charts as Chart.yaml Dependencies

Example Renovate configurations that update Chainguard chart references in
the `dependencies:` section of a `Chart.yaml`.

Sample `Chart.yaml`:

```yaml
apiVersion: v2
name: my-app
version: 0.1.0
dependencies:
  - name: nginx
    version: 22.1.0
    repository: oci://cgr.dev/my-org/iamguarded-charts
  - name: kube-prometheus-stack
    version: 87.3.0
    repository: oci://cgr.dev/my-org/charts
```

Helm's `Chart.yaml` dependency schema [has no digest
field](https://github.com/helm/helm/issues/32133), so pins here
are version-only.

## Without Cooldown

If you aren't interested in cooldown, then you only need to use the custom
changelog URL functionality of the datasource.

To use this example yourself, replace every instance of `cgr.dev/my-org`
with your own Chainguard organization name or internal mirror/proxy.

```jsonc
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "packageRules": [
    {
      "matchManagers": ["helmv3"],
      "matchDatasources": ["docker"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/charts/"],
      "changelogUrl": "http://<datasource-host>/charts/{{{replace \"cgr.dev/my-org/charts/\" \"\" packageName}}}/diff/{{currentValue}}/{{newValue}}"
    },
    {
      "matchManagers": ["helmv3"],
      "matchDatasources": ["docker"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/iamguarded-charts/"],
      "changelogUrl": "http://<datasource-host>/iamguarded-charts/{{{replace \"cgr.dev/my-org/iamguarded-charts/\" \"\" packageName}}}/diff/{{currentValue}}/{{newValue}}"
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
    "chainguard-chart": {
      "defaultRegistryUrlTemplate": "http://<datasource-host>/v1/charts/{{packageName}}/releases?cooldown=168h",
      "format": "json"
    },
    "chainguard-iamguarded-chart": {
      "defaultRegistryUrlTemplate": "http://<datasource-host>/v1/iamguarded-charts/{{packageName}}/releases?cooldown=168h",
      "format": "json"
    }
  },
  "packageRules": [
    {
      "matchManagers": ["helmv3"],
      "matchDatasources": ["docker"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/charts/"],
      "overrideDatasource": "custom.chainguard-chart",
      "overridePackageName": "{{{replace \"cgr.dev/my-org/charts/\" \"\" packageName}}}"
    },
    {
      "matchManagers": ["helmv3"],
      "matchDatasources": ["docker"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/iamguarded-charts/"],
      "overrideDatasource": "custom.chainguard-iamguarded-chart",
      "overridePackageName": "{{{replace \"cgr.dev/my-org/iamguarded-charts/\" \"\" packageName}}}"
    },
    {
      "matchDatasources": ["custom.chainguard-chart"],
      "versioning": "semver",
      "changelogUrl": "http://<datasource-host>/charts/{{packageName}}/diff/{{currentValue}}/{{newValue}}"
    },
    {
      "matchDatasources": ["custom.chainguard-iamguarded-chart"],
      "versioning": "semver",
      "changelogUrl": "http://<datasource-host>/iamguarded-charts/{{packageName}}/diff/{{currentValue}}/{{newValue}}"
    }
  ]
}
```
