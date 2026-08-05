# Updating Chainguard Helm Charts in Helmfiles

Example Renovate configurations that update Chainguard chart references in a
[`helmfile.yaml`](https://helmfile.readthedocs.io/). 

For instance:

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

## Without Cooldown

If you aren't interested in cooldown, then you only need to use the custom
changelog URL functionality of the datasource.

The `helmfile` manager in Renovate doesn't presently support updating digests,
which is a best practice when using Chainguard Helm charts, so we use custom
managers instead that extract the digest and update them using the built in
`docker` datasource.

To use this example yourself, replace every instance of `cgr.dev/my-org` with
your own Chainguard organization name or internal mirror/proxy.

```jsonc
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "packageRules": [
    {
      "matchManagers": ["helmfile"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/(charts|iamguarded-charts)/"],
      "enabled": false
    },
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
  ],
  "customManagers": [
    {
      "customType": "jsonata",
      "fileFormat": "yaml",
      "fileMatch": ["(^|/)helmfile\\.ya?ml$"],
      "matchStrings": [
        "releases[$contains(chart, 'cgr.dev/my-org/charts/')].($n := $substringAfter($substringBefore(chart & '@', '@'), 'charts/'); $exists(version) ? { 'depName': $n, 'packageName': 'cgr.dev/my-org/charts/' & $n, 'currentValue': version, 'currentDigest': $substringAfter(chart, '@') } : { 'depName': $n, 'packageName': 'cgr.dev/my-org/charts/' & $n, 'currentDigest': $substringAfter(chart, '@') })"
      ],
      "datasourceTemplate": "docker"
    },
    {
      "customType": "jsonata",
      "fileFormat": "yaml",
      "fileMatch": ["(^|/)helmfile\\.ya?ml$"],
      "matchStrings": [
        "releases[$contains(chart, 'cgr.dev/my-org/iamguarded-charts/')].($n := $substringAfter($substringBefore(chart & '@', '@'), 'iamguarded-charts/'); $exists(version) ? { 'depName': $n, 'packageName': 'cgr.dev/my-org/iamguarded-charts/' & $n, 'currentValue': version, 'currentDigest': $substringAfter(chart, '@') } : { 'depName': $n, 'packageName': 'cgr.dev/my-org/iamguarded-charts/' & $n, 'currentDigest': $substringAfter(chart, '@') })"
      ],
      "datasourceTemplate": "docker"
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
      "matchManagers": ["helmfile"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/(charts|iamguarded-charts)/"],
      "enabled": false
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
  ],
  "customManagers": [
    {
      "customType": "jsonata",
      "fileFormat": "yaml",
      "fileMatch": ["(^|/)helmfile\\.ya?ml$"],
      "matchStrings": [
        "releases[$contains(chart, 'cgr.dev/my-org/charts/')].($n := $substringAfter($substringBefore(chart & '@', '@'), 'charts/'); $exists(version) ? { 'depName': $n, 'packageName': $n, 'currentValue': version, 'currentDigest': $substringAfter(chart, '@') } : { 'depName': $n, 'packageName': $n, 'currentDigest': $substringAfter(chart, '@') })"
      ],
      "datasourceTemplate": "custom.chainguard-chart"
    },
    {
      "customType": "jsonata",
      "fileFormat": "yaml",
      "fileMatch": ["(^|/)helmfile\\.ya?ml$"],
      "matchStrings": [
        "releases[$contains(chart, 'cgr.dev/my-org/iamguarded-charts/')].($n := $substringAfter($substringBefore(chart & '@', '@'), 'iamguarded-charts/'); $exists(version) ? { 'depName': $n, 'packageName': $n, 'currentValue': version, 'currentDigest': $substringAfter(chart, '@') } : { 'depName': $n, 'packageName': $n, 'currentDigest': $substringAfter(chart, '@') })"
      ],
      "datasourceTemplate": "custom.chainguard-iamguarded-chart"
    }
  ]
}
```
