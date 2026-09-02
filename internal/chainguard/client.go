package chainguard

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"chainguard.dev/sdk/auth"
	delegate "chainguard.dev/go-grpc-kit/pkg/options"
	common "chainguard.dev/sdk/proto/platform/common/v1"
	iam "chainguard.dev/sdk/proto/platform/iam/v1"
	registry "chainguard.dev/sdk/proto/platform/registry/v1"
	"chainguard.dev/sdk/sts"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/oauth"
)

const (
	apiURL      = "https://console-api.enforce.dev"
	audience    = apiURL
	issuer      = "https://issuer.enforce.dev"
	apkAudience = "apk.cgr.dev"
)

// ErrRepoNotFound is returned by ListTags when the requested repo doesn't
// exist within the configured org.
var ErrRepoNotFound = errors.New("repo not found")

type authMode int

const (
	authChainctl authMode = iota
	authIdentity
)

type Client struct {
	OrgName  string
	OrgUIDP  string
	iam      iam.Clients
	registry registry.Clients
	conn     *grpc.ClientConn
	mode     authMode

	// baseTS is the raw OIDC token source, before any STS exchange.
	// APKTokenSource wraps it in an apk.cgr.dev-audience exchange for
	// the identity path.
	baseTS    oauth2.TokenSource
	identity  string // assumable identity UIDP for identity mode; "" for chainctl
	tokenFile string // path to OIDC token file, if identity-mode + file-based
}

// APKTokenSource returns an oauth2.TokenSource that mints apk.cgr.dev-
// audience tokens used to fetch private .apk artifacts from
// https://apk.cgr.dev/<org>/<arch>/<name>-<version>.apk.
//
// In identity mode this is an STS exchange against the configured
// assumable identity. In chainctl mode it defers to the SDK's chainctl
// token machinery (auth.NewChainctlTokenSource), which loads from the
// chainctl token cache and re-issues as needed. Callers should surface
// any error returned by Token() rather than silently degrading.
func (c *Client) APKTokenSource(ctx context.Context) oauth2.TokenSource {
	switch c.mode {
	case authIdentity:
		xchg := sts.New(issuer, apkAudience, sts.WithIdentity(c.identity))
		return sts.NewContextTokenSource(ctx, c.baseTS, xchg)
	default:
		return auth.NewChainctlTokenSource(ctx, auth.WithAudience(apkAudience))
	}
}

// New creates a Client scoped to a single Chainguard org. orgName is resolved
// to its group UIDP at construction time and all subsequent calls are scoped
// under it.
//
// The returned client uses a refreshing oauth2.TokenSource for both auth
// modes: tokens are re-read from disk (chainctl cache or projected OIDC
// token file) and re-exchanged with STS (identity mode) automatically when
// the cached Chainguard token expires. There is no need to restart the
// process when tokens rotate.
func New(ctx context.Context, orgName string, opts ...Option) (*Client, error) {
	if orgName == "" {
		return nil, errors.New("orgName is required")
	}
	o := options{mode: authChainctl}
	for _, fn := range opts {
		fn(&o)
	}

	// Per-mode auth setup: build the platform-audience token source
	// attached to the gRPC connection. In identity mode we also hold on
	// to the un-exchanged base so APKTokenSource can trade it for an
	// apk.cgr.dev-audience token; chainctl mode goes through the SDK's
	// chainctl helper directly instead.
	var (
		ts        oauth2.TokenSource
		baseTS    oauth2.TokenSource
		identity  string
		tokenFile string
	)
	switch o.mode {
	case authChainctl:
		// Fail fast at startup with an actionable message.
		if _, err := loadChainctlToken(); err != nil {
			return nil, err
		}
		ts = oauth2.ReuseTokenSource(nil, &chainctlTokenSource{})

	case authIdentity:
		if o.identity == "" || o.identityToken == "" {
			return nil, errors.New("WithIdentity requires both identity UIDP and token")
		}
		base, file, err := buildBaseTokenSource(o.identityToken)
		if err != nil {
			return nil, err
		}
		baseTS, tokenFile, identity = base, file, o.identity
		xchg := sts.New(issuer, audience, sts.WithIdentity(o.identity))
		ts = oauth2.ReuseTokenSource(nil, sts.NewContextTokenSource(ctx, base, xchg))
	}

	// Shared dial-and-resolve: one gRPC connection, dynamic per-RPC
	// credentials from the oauth2.TokenSource, IAM + Registry clients
	// sharing the same conn.
	uri, err := url.Parse(apiURL)
	if err != nil {
		return nil, fmt.Errorf("invalid API URL: %w", err)
	}
	target, dialOpts := delegate.GRPCOptions(*uri)
	dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(oauth.TokenSource{TokenSource: ts}))
	if o.concurrency > 0 {
		dialOpts = append(dialOpts, grpc.WithUnaryInterceptor(rateLimitInterceptor(o.concurrency)))
	}

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", target, err)
	}

	iamc := iam.NewClientsFromConnection(conn)
	regc := registry.NewClientsFromConnection(conn)

	orgUIDP, err := resolveOrgUIDP(ctx, iamc, orgName)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &Client{
		OrgName:   orgName,
		OrgUIDP:   orgUIDP,
		iam:       iamc,
		registry:  regc,
		conn:      conn,
		mode:      o.mode,
		baseTS:    baseTS,
		identity:  identity,
		tokenFile: tokenFile,
	}, nil
}

func (c *Client) Close() error {
	errs := []error{c.iam.Close(), c.registry.Close()}
	if c.conn != nil {
		errs = append(errs, c.conn.Close())
	}
	return errors.Join(errs...)
}

// Ready reports whether the credential material is currently available.
//
// For chainctl mode this re-reads the on-disk token cache; if the operator
// has run `chainctl auth login` since startup, this will see the fresh token
// and so will the gRPC client (which re-calls TokenSource.Token on expiry).
//
// For identity mode it stats the OIDC token file if file-based; for literal
// tokens it returns nil — there's no async credential we can probe without
// making a request.
func (c *Client) Ready(_ context.Context) error {
	if c.mode == authIdentity {
		if c.tokenFile == "" {
			return nil
		}
		if _, err := os.Stat(c.tokenFile); err != nil {
			return fmt.Errorf("identity token file unreadable: %w", err)
		}
		return nil
	}
	_, err := loadChainctlToken()
	return err
}

func resolveOrgUIDP(ctx context.Context, iamc iam.Clients, orgName string) (string, error) {
	resp, err := iamc.Groups().List(ctx, &iam.GroupFilter{Name: orgName})
	if err != nil {
		return "", fmt.Errorf("listing groups for %q: %w", orgName, err)
	}
	switch len(resp.GetItems()) {
	case 0:
		return "", fmt.Errorf("no Chainguard org found with name %q", orgName)
	case 1:
		return resp.GetItems()[0].GetId(), nil
	default:
		return "", fmt.Errorf("multiple Chainguard orgs match %q; configure with a more specific name", orgName)
	}
}

// resolveRepoUIDP walks the group hierarchy for a slash-separated
// repoName. Intermediate segments (e.g. "charts" in "charts/foo") are
// resolved as subgroups; the final segment as the repo.
func (c *Client) resolveRepoUIDP(ctx context.Context, repoName string) (string, error) {
	segments := strings.Split(repoName, "/")
	parent := c.OrgUIDP
	for _, seg := range segments[:len(segments)-1] {
		next, err := c.resolveSubgroupUIDP(ctx, parent, seg)
		if err != nil {
			return "", err
		}
		parent = next
	}
	resp, err := c.registry.Registry().ListRepos(ctx, &registry.RepoFilter{
		Uidp: &common.UIDPFilter{ChildrenOf: parent},
		Name: segments[len(segments)-1],
	})
	if err != nil {
		return "", fmt.Errorf("listing repos: %w", err)
	}
	items := resp.GetItems()
	if len(items) == 0 {
		return "", fmt.Errorf("%w: %s", ErrRepoNotFound, repoName)
	}
	return items[0].GetId(), nil
}

func (c *Client) resolveSubgroupUIDP(ctx context.Context, parentUIDP, name string) (string, error) {
	resp, err := c.iam.Groups().List(ctx, &iam.GroupFilter{
		Uidp: &common.UIDPFilter{ChildrenOf: parentUIDP},
		Name: name,
	})
	if err != nil {
		return "", fmt.Errorf("listing subgroups: %w", err)
	}
	items := resp.GetItems()
	if len(items) == 0 {
		return "", fmt.Errorf("%w: subgroup %s not found under %s", ErrRepoNotFound, name, parentUIDP)
	}
	return items[0].GetId(), nil
}

// ListTags returns the current tags for repoName within the configured org.
// Referrer (`sha256-*`) tags are filtered out by the upstream API.
func (c *Client) ListTags(ctx context.Context, repoName string) ([]Tag, error) {
	repoUIDP, err := c.resolveRepoUIDP(ctx, repoName)
	if err != nil {
		return nil, err
	}
	resp, err := c.registry.Registry().ListTags(ctx, &registry.TagFilter{
		Uidp:             &common.UIDPFilter{ChildrenOf: repoUIDP},
		ExcludeReferrers: true,
	})
	if err != nil {
		return nil, fmt.Errorf("listing tags: %w", err)
	}

	out := make([]Tag, 0, len(resp.GetItems()))
	for _, t := range resp.GetItems() {
		out = append(out, Tag{
			ID:          t.GetId(),
			Name:        t.GetName(),
			LastUpdated: t.GetLastUpdated().AsTime(),
			Digest:      t.GetDigest(),
		})
	}
	return out, nil
}

// ListAllRepos enumerates every repo in the configured org, walking
// subgroups recursively. Each returned entry is a slash-joined path
// relative to the org (e.g. "python", "charts/nginx",
// "iamguarded-charts/postgresql") — the same shape the /v1/repo
// endpoint accepts.
//
// The walk is sequential: one IAM.Groups().List per group plus one
// Registry().ListRepos per group. That's fine at typical org sizes
// (a few hundred repos, a handful of subgroups). If throughput ever
// matters, subgroup traversal can fan out via an errgroup.
func (c *Client) ListAllRepos(ctx context.Context) ([]string, error) {
	out, err := walkRepos(ctx, c.OrgUIDP, c.listRepos, c.listSubgroups)
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func (c *Client) listRepos(ctx context.Context, parentUIDP string) ([]string, error) {
	resp, err := c.registry.Registry().ListRepos(ctx, &registry.RepoFilter{
		Uidp: &common.UIDPFilter{ChildrenOf: parentUIDP},
	})
	if err != nil {
		return nil, err
	}
	items := resp.GetItems()
	names := make([]string, 0, len(items))
	for _, r := range items {
		names = append(names, r.GetName())
	}
	return names, nil
}

func (c *Client) listSubgroups(ctx context.Context, parentUIDP string) ([]groupChild, error) {
	resp, err := c.iam.Groups().List(ctx, &iam.GroupFilter{
		Uidp: &common.UIDPFilter{ChildrenOf: parentUIDP},
	})
	if err != nil {
		return nil, err
	}
	items := resp.GetItems()
	children := make([]groupChild, 0, len(items))
	for _, g := range items {
		children = append(children, groupChild{ID: g.GetId(), Name: g.GetName()})
	}
	return children, nil
}

// ListTagHistory returns historical iterations of the tag identified by tagID
// (the UIDP returned in Tag.ID).
func (c *Client) ListTagHistory(ctx context.Context, tagID string) ([]TagHistory, error) {
	if tagID == "" {
		return nil, fmt.Errorf("ListTagHistory: empty tag ID")
	}
	resp, err := c.registry.Registry().ListTagHistory(ctx, &registry.TagHistoryFilter{
		ParentId: tagID,
	})
	if err != nil {
		return nil, fmt.Errorf("listing tag history for %s: %w", tagID, err)
	}
	out := make([]TagHistory, 0, len(resp.GetItems()))
	for _, h := range resp.GetItems() {
		out = append(out, TagHistory{
			UpdateTimestamp: h.GetUpdateTimestamp().AsTime(),
			Digest:          h.GetDigest(),
		})
	}
	return out, nil
}
