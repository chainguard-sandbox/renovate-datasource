# Updating Chainguard Helm Charts in Flux

Example Renovate configuration that updates Chainguard chart references in
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

To use this example:

- Replace `<datasource-host>` with the hostname of the datasource running
  in your environment.
- Adjust the cooldown window by changing the `cooldown=168h` query parameter,
  or drop it entirely to disable per-request cooldown.
- Replace every instance of `cgr.dev/my-org` with your own Chainguard
  organization name or internal mirror/proxy.

```jsonc
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "flux": {
    "fileMatch": ["(^|/)flux\\.ya?ml$", "(^|/)gotk-components\\.ya?ml$"]
  },
  "customDatasources": {
    "chainguard-repo": {
      "defaultRegistryUrlTemplate": "http://<datasource-host>/v1/repo/{{packageName}}/releases?cooldown=168h",
      "format": "json"
    }
  },
  "packageRules": [
    {
      "matchManagers": ["flux"],
      "matchDatasources": ["docker"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/(charts|iamguarded-charts)/"],
      "overrideDatasource": "custom.chainguard-repo",
      "overridePackageName": "{{{replace \"cgr.dev/my-org/\" \"\" packageName}}}"
    },
    {
      "matchDatasources": ["custom.chainguard-repo"],
      "matchPackagePatterns": ["^(charts|iamguarded-charts)/"],
      "versioning": "semver"
    }
  ]
}
```
