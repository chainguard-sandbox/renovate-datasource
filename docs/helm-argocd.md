# Updating Chainguard Helm Charts in ArgoCD Applications

Example Renovate configuration that updates Chainguard chart references in
an [ArgoCD `Application`](https://argo-cd.readthedocs.io/en/latest/user-guide/oci/)
manifest.

Sample manifest:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: nginx
  namespace: argocd
spec:
  source:
    repoURL: oci://cgr.dev/my-org/iamguarded-charts
    chart: nginx
    targetRevision: 22.1.0@sha256:7b88d44da254fc764171da809471d10c6cf15b9ab0ddcb4b475b9a8f380aeb79
  destination:
    server: https://kubernetes.default.svc
    namespace: nginx
```

The `argocd` manager in Renovate doesn't presently support updating digests,
which is a best practice when using Chainguard Helm charts, so we disable it
for Chainguard charts and use custom managers that extract the version and
digest together.

> [!NOTE]
> Pin `targetRevision` as `<version>@<digest>`, not as a bare digest.
> Renovate's custom-datasource path needs a `currentValue` to anchor the
> lookup; a `sha256:…`-only `targetRevision` gets extracted but Renovate
> can't propose a new digest without a version to compare against.

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
      "matchManagers": ["argocd"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/(charts|iamguarded-charts)/"],
      "enabled": false
    },
    {
      "matchDatasources": ["custom.chainguard-chart", "custom.chainguard-iamguarded-chart"],
      "versioning": "semver"
    }
  ],
  "customManagers": [
    {
      "customType": "jsonata",
      "fileFormat": "yaml",
      "fileMatch": ["\\.ya?ml$"],
      "matchStrings": [
        "spec.source[$contains(repoURL, 'cgr.dev/my-org/charts')].($tr := targetRevision; $substring($tr, 0, 7) = 'sha256:' ? { 'depName': chart, 'packageName': chart, 'currentDigest': $tr } : { 'depName': chart, 'packageName': chart, 'currentValue': $substringBefore($tr & '@', '@'), 'currentDigest': $substringAfter($tr, '@') })"
      ],
      "datasourceTemplate": "custom.chainguard-chart"
    },
    {
      "customType": "jsonata",
      "fileFormat": "yaml",
      "fileMatch": ["\\.ya?ml$"],
      "matchStrings": [
        "spec.source[$contains(repoURL, 'cgr.dev/my-org/iamguarded-charts')].($tr := targetRevision; $substring($tr, 0, 7) = 'sha256:' ? { 'depName': chart, 'packageName': chart, 'currentDigest': $tr } : { 'depName': chart, 'packageName': chart, 'currentValue': $substringBefore($tr & '@', '@'), 'currentDigest': $substringAfter($tr, '@') })"
      ],
      "datasourceTemplate": "custom.chainguard-iamguarded-chart"
    }
  ]
}
```
