# Updating Chainguard Helm Charts in ArgoCD Applications

Example Renovate configurations that update Chainguard chart references in
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

## Without Cooldown

If you aren't interested in cooldown, then you only need to use the custom
changelog URL functionality of the datasource.

The `argocd` manager in Renovate doesn't presently support updating digests,
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
      "matchManagers": ["argocd"],
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
      "fileMatch": ["\\.ya?ml$"],
      "matchStrings": [
        "spec.source[$contains(repoURL, 'cgr.dev/my-org/charts')].($tr := targetRevision; $substring($tr, 0, 7) = 'sha256:' ? { 'depName': chart, 'packageName': 'cgr.dev/my-org/charts/' & chart, 'currentDigest': $tr } : { 'depName': chart, 'packageName': 'cgr.dev/my-org/charts/' & chart, 'currentValue': $substringBefore($tr & '@', '@'), 'currentDigest': $substringAfter($tr, '@') })"
      ],
      "datasourceTemplate": "docker"
    },
    {
      "customType": "jsonata",
      "fileFormat": "yaml",
      "fileMatch": ["\\.ya?ml$"],
      "matchStrings": [
        "spec.source[$contains(repoURL, 'cgr.dev/my-org/iamguarded-charts')].($tr := targetRevision; $substring($tr, 0, 7) = 'sha256:' ? { 'depName': chart, 'packageName': 'cgr.dev/my-org/iamguarded-charts/' & chart, 'currentDigest': $tr } : { 'depName': chart, 'packageName': 'cgr.dev/my-org/iamguarded-charts/' & chart, 'currentValue': $substringBefore($tr & '@', '@'), 'currentDigest': $substringAfter($tr, '@') })"
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
      "matchManagers": ["argocd"],
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
