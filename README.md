# Renovate Datasource

> [!WARNING]
> This project is developed by the Chainguard Professional Services team and maintenance is provided on a best-effort basis.

A [Renovate custom datasource](https://docs.renovatebot.com/modules/datasource/custom/)
for Chainguard images, Helm charts and APK packages.

This primarily supports two use cases:

- **`minimumReleaseAge` support.** Renovate's built-in
  [`docker` datasource](https://docs.renovatebot.com/modules/datasource/docker/)
  doesn't honour
  [`minimumReleaseAge`](https://docs.renovatebot.com/key-concepts/minimum-release-age/#which-datasources-support-release-timestamps)
  for registries other than Docker Hub, so this datasource fills the gap by
  only surfacing updates at least *N* time in the past.
  - Pairs nicely with Chainguard's optional [`cooldown`
    policy](https://edu.chainguard.dev/chainguard/chainguard-repository/container-policies/#cooldown)
    for containers, which blocks pulls at the registry level.
- **APK package versions.** Renovate has no native support for Chainguard's APK
  packages, so pinned `apk add pkg=version` lines can't be kept up to date
  without a custom datasource like this one.

## Documentation

### Datasources

Documentation that explains how each datasource works and the relevant
configuration options.

- [`repo`](docs/repo-datasource.md) - OCI repositories (images and helm charts)
- [`apk`](docs/apk-datasource.md) - APK packages

### Configuring Renovate

Examples of configuring Renovate to use the datasource.

- [Updating Chainguard Images in Dockerfiles](docs/repo-image-dockerfile.md)
- [Updating APK Package Versions in Dockerfiles](docs/apk-dockerfile.md)
- [Updating Chainguard Helm Charts in ArgoCD Applications](docs/repo-helm-argocd.md)
- [Updating Chainguard Helm Charts as Chart.yaml Dependencies](docs/repo-helm-dependency.md)
- [Updating Chainguard Helm Charts in Flux](docs/repo-helm-flux.md)
- [Updating Chainguard Helm Charts in Helmfiles](docs/repo-helm-helmfile.md)

See [Using Renovate with Chainguard Containers](https://edu.chainguard.dev/chainguard/chainguard-images/staying-secure/updating-images/renovate/)
on Chainguard Academy for general guidance on using Renovate with Chainguard.

## Build

```
go build ./cmd/renovate-datasource
```

Or the container image:

```
docker build -t renovate-datasource:dev .
```

## Run

### Serve

Starts a local webserver on `:8080` that hosts the datasources.

```
./renovate-datasource serve --org=my.org.com
```

Provides two endpoints, one for the APK packages and another for the images and
charts which are hosted as artifacts in OCI repositories under `cgr.dev`.


```
/v1/apk/{packageName}/releases
/v1/repo/{packageName}/releases
```

Pass `--datasource` to serve just one — see the
[`apk`](docs/apk-datasource.md) and [`repo`](docs/repo-datasource.md)
docs for the options specific to each:

```
./renovate-datasource serve --org=my.org.com --datasource=apk
./renovate-datasource serve --org=my.org.com --datasource=repo
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

$ curl -sf http://localhost:8080/v1/repo/python/releases
{
  "releases": [
    {
      "version": "3.14.6",
      "releaseTimestamp": "2026-06-14T18:46:31.317Z",
      "digest": "sha256:d5312494fbc793de620941d10e2bc04f0c2ce67706b9da2071b297474218c719"
    },
    {
      "version": "3.14.5",
      "releaseTimestamp": "2026-06-10T20:18:06.42Z",
      "digest": "sha256:163cc24b066e0ea18daa4966227cdb8e61c2cf9f49681bc566459506901533a6"
    }
  ]
}
```

Point Renovate at the URLs with [custom
datasources](https://docs.renovatebot.com/modules/datasource/custom/):

```json
{
  "customDatasources": {
    "chainguard-apk": {
      "defaultRegistryUrlTemplate": "http://localhost:8080/v1/apk/{{packageName}}/releases",
      "format": "json"
    },
    "chainguard-repo": {
      "defaultRegistryUrlTemplate": "http://localhost:8080/v1/repo/{{packageName}}/releases",
      "format": "json"
    }
  }
}
```

### Snapshot

Writes the datasource responses to a static folder tree for hosting behind any
web server. Typically run on a schedule.

```
./renovate-datasource snapshot --org=my.org.com -d /tmp/snap
```

Pass `--datasource` to snapshot just one — see the
[`apk`](docs/apk-datasource.md) and [`repo`](docs/repo-datasource.md)
docs for the options specific to each:

```
./renovate-datasource snapshot --org=my.org.com --datasource=apk -d /tmp/snap
./renovate-datasource snapshot --org=my.org.com --datasource=repo -d /tmp/snap
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

Point Renovate at the hosted files via [custom
datasources](https://docs.renovatebot.com/modules/datasource/custom/):

```json
{
  "customDatasources": {
    "chainguard-apk": {
      "defaultRegistryUrlTemplate": "https://<static-host>/apk/{{packageName}}/releases.json",
      "format": "json"
    },
    "chainguard-repo": {
      "defaultRegistryUrlTemplate": "https://<static-host>/repo/{{packageName}}/releases.json",
      "format": "json"
    }
  }
}
```

## Deploying to Kubernetes

Install the provided [Helm chart](chart/). Create an assumable
identity with the `registry.pull` and `apk.pull` roles (see
[Chainguard Academy](https://edu.chainguard.dev/chainguard/administration/assumable-ids/identity-examples/kubernetes-identity/))
and pass its UIDP:

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

Omit the `ingress.*` flags to skip Ingress creation (e.g. behind a
mesh, a `LoadBalancer` Service, or `kubectl port-forward`).
