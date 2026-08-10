# Updating Chainguard Helm Charts in Helmfiles

Example Renovate configuration that updates Chainguard chart references in
a [`helmfile.yaml`](https://helmfile.readthedocs.io/).

> [!NOTE]
> If you aren't interested in `minimumReleaseAge`, configure Renovate
> as described in
> ["Updating Chainguard Helm Charts in Helmfiles"](https://edu.chainguard.dev/chainguard/chainguard-images/staying-secure/updating-images/renovate/#updating-chainguard-helm-charts-in-helmfiles)
> on Chainguard Academy.

Sample manifest:

```yaml
releases:
  - name: kube-prometheus-stack
    chart: oci://cgr.dev/my-org/charts/kube-prometheus-stack@sha256:833bd55297054df0afdbe47750013b8e2eff930059c63c0746447fa8d0b729d3
    version: 87.4.0
    namespace: monitoring
  - name: nginx
    chart: oci://cgr.dev/my-org/iamguarded-charts/nginx@sha256:7b88d44da254fc764171da809471d10c6cf15b9ab0ddcb4b475b9a8f380aeb79
    version: 22.1.0
    namespace: nginx
```

The `helmfile` manager in Renovate doesn't presently support updating digests,
which is a best practice when using Chainguard Helm charts, so we disable it
for Chainguard charts and use custom managers that extract the version and
digest together.

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
      "defaultRegistryUrlTemplate": "http://<datasource-host>/v1/repo/{{packageName}}/releases?minimumReleaseAge=168h",
      "format": "json"
    }
  },
  "packageRules": [
    {
      "matchManagers": ["helmfile"],
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
      "managerFilePatterns": ["/(^|/)helmfile\\.ya?ml$/"],
      "matchStrings": [
        "releases[$contains(chart, 'cgr.dev/my-org/charts/') or $contains(chart, 'cgr.dev/my-org/iamguarded-charts/')].($pkg := $substringAfter($substringBefore(chart & '@', '@'), 'my-org/'); $name := $substringAfter($pkg, '/'); $exists(version) ? { 'depName': $name, 'packageName': $pkg, 'currentValue': version, 'currentDigest': $substringAfter(chart, '@') } : { 'depName': $name, 'packageName': $pkg, 'currentDigest': $substringAfter(chart, '@') })"
      ],
      "datasourceTemplate": "custom.chainguard-repo"
    }
  ]
}
```
