# Updating Chainguard Images in Dockerfiles

An example Renovate configuration that updates references to Chainguard images
using the custom datasource.

To use this yourself, make the following changes:

- Replace every instance of `<datasource-host>` with the hostname of the
  datasource running in your environment.
- Remove `?cooldown=168h` from the `defaultRegistryUrlTemplate` if you
  aren't interested in cooldown.
- Replace every instance of `cgr\\.dev/my-org` (or `cgr.dev/my-org`) with
  your own Chainguard organization name, or the address of your internal
  mirror/proxy.

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
      // Turn off Renovate's built-in dockerfile manager for the same
      // refs, so we don't get duplicate PRs from the built-in and
      // custom flows.
      "matchManagers": ["dockerfile"],
      "matchPackagePatterns": ["^cgr\\.dev/my-org/"],
      "enabled": false
    },
    {
      "matchDatasources": ["custom.chainguard-repo"],
      "versioning": "docker",
      "commitMessageTopic": "cgr.dev/my-org/{{depName}}",
      "changelogUrl": "http://<datasource-host>/repo/{{packageName}}/diff/{{#if currentDigest}}{{currentDigest}}{{else}}{{currentValue}}{{/if}}/{{#if newDigest}}{{newDigest}}{{else}}{{newValue}}{{/if}}"
    }
  ],
  "customManagers": [
    {
      "customType": "regex",
      "fileMatch": ["(^|/)Dockerfile$"],
      "matchStrings": [
        "FROM\\s+cgr\\.dev/my-org/(?<packageName>[A-Za-z0-9._/-]+):(?<currentValue>[A-Za-z0-9._-]+)(@(?<currentDigest>sha256:[a-f0-9]+))?"
      ],
      "datasourceTemplate": "custom.chainguard-repo"
    }
  ]
}
```
