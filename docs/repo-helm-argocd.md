# Updating Chainguard Helm Charts in ArgoCD Applications

Example Renovate configuration that updates Chainguard chart references in
an [ArgoCD `Application`](https://argo-cd.readthedocs.io/en/latest/user-guide/oci/)
manifest.

> [!NOTE]
> If you aren't interested in `minimumReleaseAge`, configure Renovate
> as described in
> ["Updating Chainguard Helm Charts in ArgoCD Applications"](https://edu.chainguard.dev/chainguard/chainguard-images/staying-secure/updating-images/renovate/#updating-chainguard-helm-charts-in-argocd-applications)
> on Chainguard Academy.

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
- Adjust the window by changing the `minimumReleaseAge=168h` query
  parameter, or drop it entirely to disable per-request overrides.
- Replace every instance of `cgr.dev/my-org` with your own Chainguard
  organization name or internal mirror/proxy.

```jsonc
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "customDatasources": {
    "chainguard-repo": {
      "defaultRegistryUrlTemplate": "https://<datasource-host>/v1/repo/{{packageName}}/releases?minimumReleaseAge=168h",
      "format": "json"
    }
  },
  "packageRules": [
    {
      "matchManagers": ["argocd"],
      "matchPackageNames": ["/^cgr\\.dev/my-org/(charts|iamguarded-charts)//"],
      "enabled": false
    },
    {
      "matchDatasources": ["custom.chainguard-repo"],
      "matchPackageNames": ["/^(charts|iamguarded-charts)//"],
      "versioning": "semver"
    }
  ],
  "customManagers": [
    {
      "customType": "jsonata",
      "fileFormat": "yaml",
      "managerFilePatterns": ["/\\.ya?ml$/"],
      "matchStrings": [
        "spec.source[$contains(repoURL, 'cgr.dev/my-org/charts') or $contains(repoURL, 'cgr.dev/my-org/iamguarded-charts')].($sub := $substringAfter(repoURL, 'my-org/'); $tr := targetRevision; $substring($tr, 0, 7) = 'sha256:' ? { 'depName': chart, 'packageName': $sub & '/' & chart, 'currentDigest': $tr } : { 'depName': chart, 'packageName': $sub & '/' & chart, 'currentValue': $substringBefore($tr & '@', '@'), 'currentDigest': $substringAfter($tr, '@') })"
      ],
      "datasourceTemplate": "custom.chainguard-repo"
    }
  ]
}
```
