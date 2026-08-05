# Updating Chainguard Helm Charts in Flux

Example Renovate configurations that update Chainguard chart references in
[Flux](https://fluxcd.io/) `OCIRepository` + `HelmRelease` manifests.

Sample manifest:

```yaml
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: kube-prometheus-stack
  namespace: monitoring
spec:
  interval: 5m
  url: oci://cgr.dev/my-org/charts/kube-prometheus-stack
  ref:
    tag: 87.4.0
    digest: sha256:833bd55297054df0afdbe47750013b8e2eff930059c63c0746447fa8d0b729d3
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: kube-prometheus-stack
  namespace: monitoring
spec:
  interval: 5m
  chartRef:
    kind: OCIRepository
    name: kube-prometheus-stack
```

## Without Cooldown

If you aren't interested in cooldown, then you only need to use the custom
changelog URL functionality of the datasource.

To use this example yourself, replace every instance of `cgr.dev/my-org` with
your own Chainguard organization name or internal mirror/proxy.

```jsonc
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "flux": {
    "fileMatch": ["(^|/)flux\\.ya?ml$", "(^|/)gotk-components\\.ya?ml$"]
  },
  "packageRules": [
    {
      "matchDatasources": ["docker"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/charts/"],
      "changelogUrl": "http://<datasource-host>/charts/{{{replace \"cgr.dev/my-org/charts/\" \"\" packageName}}}/diff/{{#if currentDigest}}{{currentDigest}}{{else}}{{currentValue}}{{/if}}/{{#if currentDigest}}{{newDigest}}{{else}}{{newValue}}{{/if}}"
    },
    {
      "matchDatasources": ["docker"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/iamguarded-charts/"],
      "changelogUrl": "http://<datasource-host>/iamguarded-charts/{{{replace \"cgr.dev/my-org/iamguarded-charts/\" \"\" packageName}}}/diff/{{#if currentDigest}}{{currentDigest}}{{else}}{{currentValue}}{{/if}}/{{#if currentDigest}}{{newDigest}}{{else}}{{newValue}}{{/if}}"
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
  "flux": {
    "fileMatch": ["(^|/)flux\\.ya?ml$", "(^|/)gotk-components\\.ya?ml$"]
  },
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
      "matchManagers": ["flux"],
      "matchDatasources": ["docker"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/charts/"],
      "overrideDatasource": "custom.chainguard-chart",
      "overridePackageName": "{{{replace \"cgr.dev/my-org/charts/\" \"\" packageName}}}"
    },
    {
      "matchManagers": ["flux"],
      "matchDatasources": ["docker"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/iamguarded-charts/"],
      "overrideDatasource": "custom.chainguard-iamguarded-chart",
      "overridePackageName": "{{{replace \"cgr.dev/my-org/iamguarded-charts/\" \"\" packageName}}}"
    },
    {
      "matchDatasources": ["custom.chainguard-chart"],
      "versioning": "semver",
      "changelogUrl": "http://<datasource-host>/charts/{{packageName}}/diff/{{#if currentDigest}}{{currentDigest}}{{else}}{{currentValue}}{{/if}}/{{#if currentDigest}}{{newDigest}}{{else}}{{newValue}}{{/if}}"
    },
    {
      "matchDatasources": ["custom.chainguard-iamguarded-chart"],
      "versioning": "semver",
      "changelogUrl": "http://<datasource-host>/iamguarded-charts/{{packageName}}/diff/{{#if currentDigest}}{{currentDigest}}{{else}}{{currentValue}}{{/if}}/{{#if currentDigest}}{{newDigest}}{{else}}{{newValue}}{{/if}}"
    }
  ]
}
```
