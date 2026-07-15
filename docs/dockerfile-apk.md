# Updating APK Package Versions in Dockerfiles

An example Renovate configuration that updates pinned APK packages in
Dockerfiles using the custom datasource.

This will update pinned packages like:

```
apk add --no-cache \
    curl=8.21.0-r1 \
    nodejs=24.14.0-r0 \
    cmd:gcloud=576.0.0-r0
```

> [!WARNING]
> Pinned APK versions may conflict with updated versions in newer
> images. Pin images by digest to minimize unexpected incompatibilities
> and keep both the packages and the images up to date with Renovate.
> Use the same cooldown for both so they move forward together.

To use this yourself, make the following changes:

- Replace `<datasource-host>` with the hostname of the datasource running
  in your environment.
- Remove `?cooldown=168h` from the `defaultRegistryUrlTemplate` if you
  aren't interested in cooldown.

```jsonc
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "customDatasources": {
    "chainguard-apk": {
      "defaultRegistryUrlTemplate": "http://<datasource-host>/v1/apk/{{packageName}}/releases?cooldown=168h",
      "format": "json"
    }
  },
  "packageRules": [
    {
      "matchDatasources": ["custom.chainguard-apk"],
      "versioning": "loose",
      "commitMessageTopic": "apk {{depName}}",
      "changelogUrl": "http://<datasource-host>/apk/{{depName}}/version/{{currentValue}}/diff/{{depName}}/version/{{newValue}}"
    }
  ],
  "customManagers": [
    {
      "customType": "regex",
      "fileMatch": ["(^|/)Dockerfile$"],
      // Scope the version match to the arguments of an `apk add`
      // invocation, tolerating backslash-continued lines so
      // multi-package installs like
      //
      //   RUN apk add --no-cache \
      //       git=2.55.0-r0 \
      //       openssl==3.5.1-r0
      //
      // produce one dep per pinned package. The alternation puts
      // the `\<newline>` branch first so the trailing backslash on a
      // continuation line isn't swallowed by `[^\n]`. The `={1,2}`
      // in the inner regex accepts either apk equality operator —
      // `=` and `==` behave identically in apk-tools.
      "matchStringsStrategy": "recursive",
      "matchStrings": [
        "apk\\s+add(?:\\\\\\n\\s*|[^\\n])*",
        "(?<depName>[a-z0-9][a-z0-9._+:-]*)={1,2}(?<currentValue>[A-Za-z0-9._+-]+)"
      ],
      "datasourceTemplate": "custom.chainguard-apk"
    }
  ]
}
```
