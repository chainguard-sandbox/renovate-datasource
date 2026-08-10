# APK Datasource

Pulls the APKINDEX for the Chainguard repositories and serves the versions for
each package over `/v1/apk/{packageName}/releases`, or writes them to
`apk/{packageName}/releases.json` in `snapshot` mode.

## Endpoints

The `{packageName}` segment is a plain package name, an unprefixed provider,
or a prefixed capability:

```
/v1/apk/curl/releases       → package curl
/v1/apk/nodejs/releases     → any package providing `nodejs` (nodejs-24, nodejs-26, …)
/v1/apk/cmd:gcloud/releases → any package providing `cmd:gcloud` (google-cloud-sdk)
```

In `snapshot` mode the same payloads are written under `apk/`:

```
apk/
├── curl/releases.json
├── nodejs/releases.json
└── cmd:gcloud/releases.json
```

Response shape:

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

The `releaseTimestamp` is taken from the `t:` field in the APKINDEX record.

## Running

See the [Authentication](#authentication) section below for instructions on
authenticating with Chainguard's APK repositories.

### Server

```
# Default: the org's three-repo chain (private + two virtualapk feeds)
./renovate-datasource serve --org=my.org.com --datasource=apk

# Custom mirror(s), no Chainguard credentials required
export HTTP_AUTH="basic:mirror.example:<user>:<password>"
./renovate-datasource serve --datasource=apk \
    --apk-repository=https://mirror.example/apk/chainguard \
    --apk-repository=https://mirror.example/apk/extra-packages
```

### Snapshot

```
# Default: the org's three-repo chain (private + two virtualapk feeds)
./renovate-datasource snapshot -d /tmp/snap --org=my.org.com --datasource=apk

# Custom mirror(s), no Chainguard credentials required
export HTTP_AUTH="basic:mirror.example:<user>:<password>"
./renovate-datasource snapshot -d /tmp/snap --datasource=apk \
    --apk-repository=https://mirror.example/apk/chainguard \
    --apk-repository=https://mirror.example/apk/extra-packages
```

## Configuration

### Default Repositories

Without `--apk-repository`, the datasource pulls three feeds derived
from `--org` and its Chainguard UIDP:

```
https://apk.cgr.dev/<org-name>                        # private
https://virtualapk.cgr.dev/<org-uidp>/chainguard      # public
https://virtualapk.cgr.dev/<org-uidp>/extra-packages  # public
```

Access to `apk.cgr.dev` requires
`chainctl auth login --audience=apk.cgr.dev` or an assumable identity
(see the [Authentication](#authentication) section below).

### Custom Repositories

The `--apk-repository=<url>` flag overrides the defaults with one or more
mirror or proxy URLs.

Each URL is a repo root which the loader appends `/{arch}/APKINDEX.tar.gz` to.

If the `repo` datasource is disabled as well, then the `--org` flag is not
required and authentication with the Chainguard APIs is skipped.

This allows you to point at the public `virtualapk.cgr.dev` repositories without
any credentials:

```
./renovate-datasource serve --datasource=apk \
    --apk-repository=https://virtualapk.cgr.dev/<org-uidp>/chainguard \
    --apk-repository=https://virtualapk.cgr.dev/<org-uidp>/extra-packages
```

### Authentication

For the default `apk.cgr.dev` feed, login with `chainctl`:

```
chainctl auth login --audience=apk.cgr.dev
```

Or, use an [assumable identity](https://edu.chainguard.dev/chainguard/administration/assumable-ids/identity-examples/kubernetes-identity/) with the `apk.pull` role:

```
./renovate-datasource serve --org=my.org.com --datasource=apk \
    --identity=<identity-uidp> \
    --identity-token=/path/to/token
```

For a `--apk-repository` mirror that needs Basic auth, set
`HTTP_AUTH`: 

```
export HTTP_AUTH="basic:<host>:<user>:<password>"
```

### Index Refresh

The `--apk-index-refresh` flag defines how often the APKINDEX is refetched when
running in `server` mode. Defaults to `1h`.

### Minimum Release Age

Set `--min-release-age=168h` to configure a minimum release age. Applies in both
`server` and `snapshot` modes.

For `server`, you can also  pass `?minimumReleaseAge=<duration>` as a query
parameter.

```
/v1/apk/curl/releases?minimumReleaseAge=168h
```

Entries whose `releaseTimestamp` is newer than the cutoff are
filtered out. If a record has no `t:` field (or `t:0`) it's dropped
whenever a cutoff is active, since we can't prove it sits outside
the window.

### Maximum Release Age

`--max-release-age=4380h` (snapshot only) drops releases whose
`releaseTimestamp` is older than the given window and skips packages
that have nothing left in the window. Useful for keeping snapshot
size bounded — the example above trims to roughly the last six
months.

### Concurrency

`--concurrency` caps the per-package fan-out when generating a
snapshot. The default is `16`. Snapshot only — `serve` lookups are
in-memory.

## Renovate Configuration

- [Updating APK Package Versions in Dockerfiles](apk-dockerfile.md)
