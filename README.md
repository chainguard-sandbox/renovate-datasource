# Renovate Datasource

> [!WARNING]
> This project is developed by the Chainguard Professional Services team and maintenance is provided on a best-effort basis.

A [Renovate custom datasource](https://docs.renovatebot.com/modules/datasource/custom/)
for Chainguard images and Helm charts.

It exists for **`minimumReleaseAge` support**. Renovate's built-in
[`docker` datasource](https://docs.renovatebot.com/modules/datasource/docker/)
doesn't honour
[`minimumReleaseAge`](https://docs.renovatebot.com/key-concepts/minimum-release-age/#which-datasources-support-release-timestamps)
for registries other than Docker Hub, so this datasource fills the gap by
only surfacing updates at least *N* time in the past.

Pairs nicely with Chainguard's optional [`cooldown`
policy](https://edu.chainguard.dev/chainguard/chainguard-repository/container-policies/#cooldown)
for containers, which blocks pulls at the registry level.

> [!NOTE]
> **For APK packages, prefer Renovate's built-in
> [`apk` datasource](https://docs.renovatebot.com/modules/datasource/apk/).**
> It reads `APKINDEX.tar.gz` directly and supports Wolfi and Chainguard
> repositories, so pinned `apk add pkg=version` lines no longer need a custom
> datasource to stay current.
>
> The `apk` datasource here still works and is still served by default, but it
> is no longer the recommended path. Everything below covers `repo` only; pass
> `--datasource=repo` to leave `apk` out entirely and skip its startup cost.
> See [`docs/apk-datasource.md`](docs/apk-datasource.md) if you still need it.

## Documentation

### Datasource

Documentation that explains how the datasource works and the relevant
configuration options.

- [`repo`](docs/repo-datasource.md) - OCI repositories (images and helm charts)

### Configuring Renovate

Examples of configuring Renovate to use the datasource.

- [Updating Chainguard Images in Dockerfiles](docs/repo-image-dockerfile.md)
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

Starts a local webserver on `:8080` that hosts the datasource.

```
./renovate-datasource serve --org=my.org.com --datasource=repo
```

Provides an endpoint for the images and charts which are hosted as artifacts in
OCI repositories under `cgr.dev`:

```
/v1/repo/{packageName}/releases
```

The datasource endpoint is then a plain HTTP GET away:

```
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

Point Renovate at the URL with [custom
datasources](https://docs.renovatebot.com/modules/datasource/custom/):

```json
{
  "customDatasources": {
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
./renovate-datasource snapshot --org=my.org.com --datasource=repo -d /tmp/snap
```

Layout:

```
/tmp/snap/
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
    "chainguard-repo": {
      "defaultRegistryUrlTemplate": "https://<static-host>/repo/{{packageName}}/releases.json",
      "format": "json"
    }
  }
}
```

## Deploying to Kubernetes

Install the provided [Helm chart](chart/). Create an assumable
identity with the `registry.pull` role (see
[Chainguard Academy](https://edu.chainguard.dev/chainguard/administration/assumable-ids/identity-examples/kubernetes-identity/))
and pass its UIDP:

```
helm install renovate-datasource ./chart \
  --create-namespace \
  --namespace chainguard \
  --set org=<your-chainguard-org> \
  --set identity=<identity-uidp> \
  --set datasources={repo} \
  --set image.repository=<your-image-repo> \
  --set image.tag=<your-image-tag> \
  --set ingress.enabled=true \
  --set ingress.className=<your-ingress-class> \
  --set ingress.hostname=<your-ingress-hostname>
```

Omit the `ingress.*` flags to skip Ingress creation (e.g. behind a
mesh, a `LoadBalancer` Service, or `kubectl port-forward`).

Leaving `datasources` unset serves the `apk` datasource too, which additionally
requires the `apk.pull` role on the identity.
