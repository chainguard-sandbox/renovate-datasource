package oci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sync/errgroup"

	cosignremote "github.com/sigstore/cosign/v3/pkg/oci/remote"
	spdx "github.com/spdx/tools-golang/spdx/v2/v2_3"
)

// ErrNoSBOM is returned by SBOMAndApkoConfig when the image exists on cgr.dev
// but has no SPDX attestation we can decode. Clients can errors.Is against it
// to give a more specific response code than the generic upstream-error case.
var ErrNoSBOM = errors.New("no SPDX SBOM attestation")

// ApkoConfigPredicateType is the in-toto predicateType used by the
// attestation apko emits when it builds an image.
const ApkoConfigPredicateType = "https://apko.dev/image-configuration"

// Fetcher pulls OCI configs and SBOMs from cgr.dev for repos in a single
// Chainguard org.
type Fetcher struct {
	orgName  string
	kc       authn.Keychain
	platform v1.Platform
}

// New constructs a Fetcher scoped to orgName. kc must authorise requests to
// cgr.dev (see Keychain).
func New(orgName string, kc authn.Keychain) *Fetcher {
	return &Fetcher{
		orgName:  orgName,
		kc:       kc,
		platform: v1.Platform{OS: "linux", Architecture: "amd64"},
	}
}

// ResolveDigest resolves repo @ ref to a per-platform manifest digest. Both
// tags and digests are passed through remote.Image with WithPlatform so that
// an index reference — whether named by tag or by its index-level digest —
// descends to a single-arch child manifest. Returning the index digest as-is
// would cause SBOMSPDX to fetch the index-level attestation, which only
// enumerates the child manifests rather than the apk packages we want.
func (f *Fetcher) ResolveDigest(ctx context.Context, repo, ref string) (string, error) {
	r, err := f.refFor(repo, ref)
	if err != nil {
		return "", err
	}
	img, err := remote.Image(r, append(f.remoteOpts(ctx), remote.WithPlatform(f.platform))...)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", r, err)
	}
	d, err := img.Digest()
	if err != nil {
		return "", err
	}
	return d.String(), nil
}

// Config fetches the OCI image config for repo @ ref at the configured
// platform. Callers typically pass a digest returned by ResolveDigest, in
// which case the platform negotiation is a no-op.
func (f *Fetcher) Config(ctx context.Context, repo, ref string) (*v1.ConfigFile, error) {
	r, err := f.refFor(repo, ref)
	if err != nil {
		return nil, err
	}
	img, err := remote.Image(r, append(f.remoteOpts(ctx), remote.WithPlatform(f.platform))...)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", r, err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", r, err)
	}
	return cf, nil
}

// SBOMAndApkoConfig fetches the attestation set for repo @ ref and decodes
// both the SPDX SBOM and the apko image-configuration predicate in a single
// pass, so the underlying signature list is fetched from cgr.dev only once
// (vs the previous "one call per attestation type" shape). Returns ErrNoSBOM
// if the image has no SPDX attestation. A nil apko byte slice signals
// "no apko image-configuration attestation" — not an error, since older or
// non-apko-built images legitimately don't carry one.
//
// ref must be a per-platform digest ("sha256:..."); call ResolveDigest first
// to obtain one from a tag. Passing a tag would let cosign descend into the
// index manifest and pick up the index-level signature set rather than the
// per-arch attestations Chainguard publishes.
func (f *Fetcher) SBOMAndApkoConfig(ctx context.Context, repo, ref string) (*spdx.Document, []byte, error) {
	r, err := f.refFor(repo, ref)
	if err != nil {
		return nil, nil, err
	}

	se, err := cosignremote.SignedEntity(r, cosignremote.WithRemoteOptions(f.remoteOpts(ctx)...))
	if err != nil {
		return nil, nil, fmt.Errorf("SignedEntity %s: %w", r, err)
	}
	atts, err := se.Attestations()
	if err != nil {
		return nil, nil, fmt.Errorf("attestations %s: %w", r, err)
	}
	sigs, err := atts.Get()
	if err != nil {
		return nil, nil, fmt.Errorf("getting attestations %s: %w", r, err)
	}

	// Attestation payload fetches are independent; parallelise them.
	// Each goroutine pulls its payload once and tries both decoders,
	// avoiding a second round of Payload() calls per signature.
	//
	// egCtx lets a failure or cancelled caller short-circuit remaining
	// goroutines before they start their Payload fetch. cosign's
	// oci.Signature.Payload doesn't accept a context itself, so the
	// cancellation only takes effect at goroutine entry.
	docs := make([]*spdx.Document, len(sigs))
	apkos := make([]json.RawMessage, len(sigs))
	eg, egCtx := errgroup.WithContext(ctx)
	for i, sig := range sigs {
		eg.Go(func() error {
			if err := egCtx.Err(); err != nil {
				return err
			}
			payload, err := sig.Payload()
			if err != nil {
				return fmt.Errorf("attestation payload: %w", err)
			}
			stmt, ok := decodeInTotoStatement(payload)
			if !ok {
				return nil
			}
			switch {
			case strings.HasPrefix(stmt.PredicateType, "https://spdx.dev/"):
				var doc spdx.Document
				if err := json.Unmarshal(stmt.Predicate, &doc); err != nil {
					return fmt.Errorf("parsing SPDX predicate: %w", err)
				}
				docs[i] = &doc
			case stmt.PredicateType == ApkoConfigPredicateType:
				apkos[i] = stmt.Predicate
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, nil, err
	}

	var doc *spdx.Document
	for _, d := range docs {
		if d != nil {
			doc = d
			break
		}
	}
	var apko json.RawMessage
	for _, p := range apkos {
		if p != nil {
			apko = p
			break
		}
	}
	if doc == nil {
		return nil, nil, fmt.Errorf("%w for %s", ErrNoSBOM, r)
	}
	return doc, apko, nil
}

func (f *Fetcher) refFor(repo, ref string) (name.Reference, error) {
	base := "cgr.dev/" + f.orgName + "/" + repo
	if strings.HasPrefix(ref, "sha256:") {
		return name.NewDigest(base + "@" + ref)
	}
	return name.NewTag(base + ":" + ref)
}

func (f *Fetcher) remoteOpts(ctx context.Context) []remote.Option {
	return []remote.Option{
		remote.WithAuthFromKeychain(f.kc),
		remote.WithContext(ctx),
	}
}

// --- attestation decoding ---

// decodeInTotoStatement unwraps a cosign attestation's DSSE envelope and
// returns the enclosed in-toto statement. Returns ok=false for any payload
// that isn't a DSSE-wrapped JSON in-toto statement — not every attestation
// on a Chainguard image matches that shape, so callers should treat "not
// ok" as "skip this signature" rather than an error.
func decodeInTotoStatement(payload []byte) (intotoStatement, bool) {
	var env dsseEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return intotoStatement{}, false
	}
	rawStmt, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return intotoStatement{}, false
	}
	var stmt intotoStatement
	if err := json.Unmarshal(rawStmt, &stmt); err != nil {
		return intotoStatement{}, false
	}
	return stmt, true
}

type dsseEnvelope struct {
	PayloadType string `json:"payloadType"`
	Payload     string `json:"payload"`
}

type intotoStatement struct {
	Type          string          `json:"_type"`
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
}
