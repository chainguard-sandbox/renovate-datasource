# Renovate Datasource

> [!WARNING]
> This project is developed by the Chainguard Professional Services team and maintenance is provided on a best-effort basis.

A [Renovate custom datasource](https://docs.renovatebot.com/modules/datasource/custom/)
for Chainguard images, Helm charts and APK packages.

This primarily supports two use cases:

- **Cooldown for images and charts.** Renovate's built-in `docker`
  datasource doesn't honour `minimumReleaseAge`; this datasource fills the
  gap by only surfacing updates at least *N* time in the past. Pairs
  nicely with Chainguard's optional [`cooldown` policy](https://edu.chainguard.dev/chainguard/chainguard-repository/container-policies/#cooldown)
  for containers, which blocks pulls at the registry level.
- **APK package versions.** Renovate has no native support for Chainguard's APK
  packages, so pinned `apk add pkg=version` lines can't be kept up to date
  without a custom datasource.

## Configuring Renovate

Examples of configuring Renovate to use the datasource.

- [Updating Chainguard Images in Dockerfiles](docs/repo-dockerfile.md)
- [Updating APK Package Versions in Dockerfiles](docs/apk-dockerfile.md)
- [Updating Chainguard Helm Charts in ArgoCD Applications](docs/helm-argocd.md)
- [Updating Chainguard Helm Charts as Chart.yaml Dependencies](docs/helm-dependency.md)
- [Updating Chainguard Helm Charts in Flux](docs/helm-flux.md)
- [Updating Chainguard Helm Charts in Helmfiles](docs/helm-helmfile.md)

## Build

```
go build ./cmd/renovate-datasource
```

Or, build the container image:

```
docker build -t renovate-datasource:dev .
```

## Run

### HTTP Server

The `serve` subcommand hosts the datasource on a local webserver.

```
# Login to Chainguard - the datasource will reuse these credentials
chainctl auth login

# Serves both /v1/repo/{packageName}/releases and /v1/apk/{packageName}/releases
./renovate-datasource serve --org=my.org.com

# Use --datasource to only serve one of the datasources
./renovate-datasource serve --org=my.org.com --datasource apk
```

You can also use an [assumable
identity](https://edu.chainguard.dev/platform/administration/assumable-ids/) to
authenticate with an OIDC token from a non-interactive workload like a
Kubernetes pod.

```
./renovate-datasource serve --org=my.org.com \
    --identity=<identity-uidp> \
    --identity-token=/path/to/token
```

The datasource endpoints are then a plain HTTP GET away:

```
$ curl -sf http://localhost:8080/v1/apk/curl/releases
{
  "releases": [
    {
      "version": "8.21.0-r1",
      "releaseTimestamp": "2026-06-30T04:35:58Z"
    },
    {
      "version": "8.21.0-r0",
      "releaseTimestamp": "2026-06-24T08:29:32Z"
    }
  ]
}
```

Point Renovate at it via a [custom
datasource](https://docs.renovatebot.com/modules/datasource/custom/):

```json
{
  "customDatasources": {
    "chainguard-apk": {
      "defaultRegistryUrlTemplate": "http://localhost:8080/v1/apk/{{packageName}}/releases",
      "format": "json"
    }
  }
}
```

### Snapshot

The `snapshot` command writes the response for every package to disk so that it
can be served by any static webserver. 

This is a one-shot command that you would typically run on a schedule, copying
the output to wherever you are hosting the datasource.

```
# Write the full snapshot to /tmp/snap
./renovate-datasource snapshot --org=my.org.com -d /tmp/snap

# Apply a cooldown to the snapshot
./renovate-datasource snapshot --org=my.org.com -d /tmp/snap \
    --cooldown=168h

# Only write the snapshot for one of the datasources (repo, apk)
./renovate-datasource snapshot --org=my.org.com -d /tmp/snap \
    --datasource apk

# Exclude versions older than a specific duration, which limits the size of the
# snapshot.
./renovate-datasource snapshot --org=my.org.com -d /tmp/snap \
    --max-release-age=4380h
```

Layout:

```
/tmp/snap/
├── apk/
│   ├── cmd:node/releases.json                     # prefixed capability
│   ├── curl/releases.json                         # apk package
│   └── nodejs/releases.json                       # unprefixed provider
└── repo/
    ├── charts/
    │   └── nginx/releases.json                    # helm chart
    ├── iamguarded-charts/
    │   └── postgresql/releases.json               # iamguarded helm chart
    └── python/releases.json                       # image
```

Point Renovate at the hosted files via a [custom
datasource](https://docs.renovatebot.com/modules/datasource/custom/):

```json
{
  "customDatasources": {
    "chainguard-apk": {
      "defaultRegistryUrlTemplate": "http://<static-host>/apk/{{packageName}}/releases.json",
      "format": "json"
    }
  }
}
```

### Kubernetes

Deploy to Kubernetes with the provided [Helm chart](chart/).

Firstly, create an assumable identity as described in [the
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

Then, install the chart, supplying your own org, identity UIDP and image details:

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
`/releases`. The query parameter, when present, overrides the flag. When
used, it provides a view of the releases as of *N* time in the past.

### Images and Helm Charts

Releases for images and helm charts are returned from the same `/v1/repo/{repo}/releases`
URL pattern.

```
/v1/repo/python/releases                       → cgr.dev/<org>/python
/v1/repo/charts/nginx/releases                 → cgr.dev/<org>/charts/nginx
/v1/repo/iamguarded-charts/postgresql/releases → cgr.dev/<org>/iamguarded-charts/postgresql
```

The datasource lists the tags with [`/registry/v1/tags`](https://edu.chainguard.dev/platform/api/spec-api-v1/#tag/registry/GET/registry/v1/tags)
and formats the results in the expected format.

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

Where the timestamp of a tag falls within the cooldown window, the tag history
API ([`/registry/v1/tags/{parentId}/history`](https://edu.chainguard.dev/platform/api/spec-api-v1/#tag/registry/GET/registry/v1/tags/{parentId}/history))
is used to rewind the tag back to the latest digest outside of the window.

If no historical digest satisfies the cooldown then the tag is omitted from the
results entirely.

### APKs

The datasource pulls the APKINDEX for each of the organization's repositories
at startup and regularly refreshes it according to the value of
`--apk-index-refresh` (default `1h`).

```
https://apk.cgr.dev/<org-name>
https://virtualapk.cgr.dev/<org-id>/chainguard
https://virtualapk.cgr.dev/<org-id>/extra-packages
```

It uses the `t:` field in the index as the `releaseTimestamp` and omits versions
where the timestamp falls inside the cooldown window.

```json
{
  "releases": [
    {
      "version": "3.14.5-r0",
      "releaseTimestamp": "2026-06-10T20:18:06.42Z"
    },
    {
      "version": "3.14.6-r1",
      "releaseTimestamp": "2026-06-14T18:46:31.317Z"
    }
  ]
}
```

This supports provides and prefixed capabilities as well:
1. For instance `nodejs` will return all versions for the packages that provide it
   (`nodejs-26`, `nodejs-25` etc)
2. And `cmd:gcloud`, which will return versions for `google-cloud-sdk`.

