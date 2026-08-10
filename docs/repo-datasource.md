# Repository Datasource

Pulls the list of tags for an OCI repository from the Chainguard API and serves
them over `/v1/repo/{repoPath}/releases`, or writes them to
`repo/{repoPath}/releases.json` in `snapshot` mode. 

## Endpoints

Path segments map directly onto `cgr.dev/<org>/<path>`:

```
/v1/repo/python/releases                       → cgr.dev/<org>/python
/v1/repo/charts/nginx/releases                 → cgr.dev/<org>/charts/nginx
/v1/repo/iamguarded-charts/postgresql/releases → cgr.dev/<org>/iamguarded-charts/postgresql
```

In `snapshot` mode the same payloads are written under `repo/`:

```
repo/
├── python/releases.json
├── charts/
│   └── nginx/releases.json
└── iamguarded-charts/
    └── postgresql/releases.json
```

Response shape:

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

Tag data comes from
[`/registry/v1/tags`](https://edu.chainguard.dev/platform/api/spec-api-v1/#tag/registry/GET/registry/v1/tags).

## Running

```
# Serve (HTTP)
./renovate-datasource serve --org=my.org.com --datasource=repo

# Snapshot (static output for hosting behind any web server)
./renovate-datasource snapshot --org=my.org.com --datasource=repo -d /tmp/snap
```

See the [Authentication](#authentication) section below for instructions on
authenticating with Chainguard's APIs.

## Configuration

### Chainguard Org

`--org` (required) is the Chainguard org/group name. Determines which
tags and repos are visible.

### Authentication

By default the datasource re-uses the caller's `chainctl` session.

```
chainctl auth login
```

For non-interactive workloads, supply an [assumable identity](https://edu.chainguard.dev/chainguard/administration/assumable-ids/identity-examples/kubernetes-identity/) with the `registry.pull` role:


```
./renovate-datasource serve --org=my.org.com --datasource=repo \
    --identity=<identity-uidp> \
    --identity-token=/path/to/token
```

### Minimum Release Age

Set `--min-release-age=168h` to configure a minimum release age. Applies in both
`server` and `snapshot` modes.

For `server`, you can also  pass `?minimumReleaseAge=<duration>` as a query
parameter.

```
/v1/repo/charts/kube-prometheus-stack/releases?minimumReleaseAge=168h
```

If a tag is more recent than the cutoff, it is rewound to the most recent digest
outside of the age window with the [`/registry/v1/tags/{parentId}/history`](https://edu.chainguard.dev/platform/api/spec-api-v1/#tag/registry/GET/registry/v1/tags/{parentId}/history) endpoint.

### Maximum Release Age

In `snapshot` mode, set `--max-release-age=4380h` to drop releases older than
the specified duration from the results. 

Use this to keep the size of the snapshot bounded by omitting data for versions
that are older and unlikely to be updated to.

### Concurrency

The `--concurrency` flag caps concurrent tag-history lookups per request. The
default is `16`.

## Renovate Configuration

- [Updating Chainguard Images in Dockerfiles](repo-image-dockerfile.md)
- [Updating Chainguard Helm Charts in ArgoCD Applications](repo-helm-argocd.md)
- [Updating Chainguard Helm Charts as Chart.yaml Dependencies](repo-helm-dependency.md)
- [Updating Chainguard Helm Charts in Flux](repo-helm-flux.md)
- [Updating Chainguard Helm Charts in Helmfiles](repo-helm-helmfile.md)
