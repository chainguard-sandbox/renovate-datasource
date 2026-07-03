# Renovate Datasource

A [Renovate custom datasource](https://docs.renovatebot.com/modules/datasource/custom/)
for Chainguard images.

It implements two features:

1. An optional cooldown that only surfaces tags and digests at least *N*
   time in the past.
2. Detailed changelog URLs, via a custom UI that diffs the old and the new
   images.

## Configuring Renovate

Here is an example that uses the datasource to update references to Chainguard
images in dockerfiles.

To use this yourself, make the following changes:
- Replace `<datasource-host>` with the hostname of the datasource running in
  your environment
- Remove `?cooldown=168h` from the `defaultRegistryUrlTemplate` if you aren't
  interested in cooldown.
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
      "datasourceTemplate": "custom.chainguard-repo",
    }
  ]
}
```

## Build

```
go build ./cmd/renovate-datasource
```

Or, build the container image:

```
docker build -t renovate-datasource:dev .
```

## Run

### Locally

Run the service locally and reuse the local credentials provided by `chainctl`:

```
# Login to Chainguard
chainctl auth login

# Run the service
./renovate-datasource --org=my.org.com
```

### Kubernetes

A Helm chart lives at [`chart/`](chart/). It renders a ServiceAccount,
Deployment, and Service; the pod uses a projected service-account token
to authenticate as a Chainguard assumable identity, so no long-lived
credentials are mounted.

Create an assumable identity as described in [the
documentation](https://edu.chainguard.dev/chainguard/administration/assumable-ids/identity-examples/kubernetes-identity/)
with the `registry.pull` and `apk.pull` roles. Assuming the service is
deployed to the `chainguard` namespace and the issuer URL of your cluster
is publicly available:

```
chainctl iam identity create renovate-datasource \
  --parent=<your-chainguard-org> \
  --identity-issuer=<your-cluster-oidc-issuer-url> \
  --subject=system:serviceaccount:chainguard:renovate-datasource \
  --role=registry.pull \
  --role=apk.pull
```

Note the printed identity UIDP — that's the value for `identity` below.

Install the chart, supplying your own org, identity UIDP and image details:

```
helm install renovate-datasource ./chart \
  --create-namespace \
  --namespace chainguard \
  --set org=<your-chainguard-org> \
  --set identity=<identity-uidp> \
  --set image.repository=<your-image-repo> \
  --set image.tag=<your-image-tag> \
  --set ingress.enabled=true \
  --set ingress.className=<your-ingress-class> \
  --set ingress.hostname=<your-ingress-hostname>
```

Omit the three `ingress.*` flags to skip Ingress (e.g. if you're exposing
the service through a mesh, a `LoadBalancer` Service (`--set
service.type=LoadBalancer`), or a `kubectl port-forward` for local testing).

## How It Works

### Cooldown

Disabled by default. Enable it either server-wide with `--cooldown=<duration>`
(e.g. `168h`) or per request via a `?cooldown=<duration>` query parameter on
`/releases`. The query parameter, when present, overrides the flag.

When used, it essentially provides a view of the tags that is *N* time in the
past.

For each tag in in the repo:

- If the tag's current digest is older than the cooldown window → return the tag, the update time, and the digest as-is.
- If the current digest is newer than the cooldown window → walk the tag's history and return the most recent digest that *was* old enough.
- If no historical digest satisfies the cooldown → omit the tag.

A `GET /v1/repo/{repo}/releases` response looks like:

```json
{
  "releases": [
    {
      "version": "3.14.5",
      "releaseTimestamp": "2026-06-10T20:18:06.42Z",
      "digest": "sha256:163cc24b066e0ea18daa4966227cdb8e61c2cf9f49681bc566459506901533a6"
    },
    {
      "version": "3.14.6",
      "releaseTimestamp": "2026-06-14T18:46:31.317Z",
      "digest": "sha256:d5312494fbc793de620941d10e2bc04f0c2ce67706b9da2071b297474218c719"
    }
  ]
}
```

### Changelogs

Visiting `https://<datasource-host>/repo/node/diff/{{currentDigest}}/{{newDigest}}`
will show a changelog that compares the differences between the two images.

It does this by fetching the image config, SBOMs and package metadata for each
image and comparing the contents.
