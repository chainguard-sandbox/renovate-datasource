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

// Statement is a decoded in-toto statement from an attestation.
type Statement struct {
	Type          string
	PredicateType string
	Predicate     json.RawMessage
}

// AttestationStatements fetches every attestation attached to repo @
// ref, DSSE-unwraps each, and returns the in-toto statements.
// Payloads that aren't DSSE-wrapped in-toto are silently skipped.
//
// ref must be a per-platform digest ("sha256:..."); call ResolveDigest
// first to obtain one from a tag. A tag would let cosign descend into
// the index manifest and pick up index-level signatures rather than
// the per-arch attestations Chainguard publishes.
func (f *Fetcher) AttestationStatements(ctx context.Context, repo, ref string) ([]Statement, error) {
	r, err := f.refFor(repo, ref)
	if err != nil {
		return nil, err
	}

	se, err := cosignremote.SignedEntity(r, cosignremote.WithRemoteOptions(f.remoteOpts(ctx)...))
	if err != nil {
		return nil, fmt.Errorf("SignedEntity %s: %w", r, err)
	}
	atts, err := se.Attestations()
	if err != nil {
		return nil, fmt.Errorf("attestations %s: %w", r, err)
	}
	sigs, err := atts.Get()
	if err != nil {
		return nil, fmt.Errorf("getting attestations %s: %w", r, err)
	}

	// cosign's oci.Signature.Payload has no context, so cancellation
	// takes effect only at goroutine entry. Cap concurrency to bound
	// fan-out on a manifest with many attestations.
	slots := make([]*Statement, len(sigs))
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(16)
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
			slots[i] = &stmt
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	out := make([]Statement, 0, len(slots))
	for _, s := range slots {
		if s != nil {
			out = append(out, *s)
		}
	}
	return out, nil
}

// SBOMAndApkoConfig returns the SPDX SBOM and apko image-configuration
// predicate from repo @ ref's attestations. Returns ErrNoSBOM if no
// SPDX attestation is present. A nil apko byte slice means the image
// has no apko attestation — legitimate for older or non-apko-built
// images.
func (f *Fetcher) SBOMAndApkoConfig(ctx context.Context, repo, ref string) (*spdx.Document, []byte, error) {
	stmts, err := f.AttestationStatements(ctx, repo, ref)
	if err != nil {
		return nil, nil, err
	}

	var doc *spdx.Document
	var apko json.RawMessage
	for _, s := range stmts {
		switch {
		case doc == nil && strings.HasPrefix(s.PredicateType, "https://spdx.dev/"):
			var d spdx.Document
			if err := json.Unmarshal(s.Predicate, &d); err != nil {
				return nil, nil, fmt.Errorf("parsing SPDX predicate: %w", err)
			}
			doc = &d
		case apko == nil && s.PredicateType == ApkoConfigPredicateType:
			apko = s.Predicate
		}
	}
	if doc == nil {
		r, _ := f.refFor(repo, ref)
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

// decodeInTotoStatement unwraps a cosign attestation's DSSE envelope
// and returns the in-toto statement inside. ok=false for any payload
// that isn't a DSSE-wrapped JSON in-toto statement — callers should
// treat that as "skip this signature" rather than an error.
func decodeInTotoStatement(payload []byte) (Statement, bool) {
	var env struct {
		PayloadType string `json:"payloadType"`
		Payload     string `json:"payload"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return Statement{}, false
	}
	rawStmt, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return Statement{}, false
	}
	var stmt struct {
		Type          string          `json:"_type"`
		PredicateType string          `json:"predicateType"`
		Predicate     json.RawMessage `json:"predicate"`
	}
	if err := json.Unmarshal(rawStmt, &stmt); err != nil {
		return Statement{}, false
	}
	return Statement{Type: stmt.Type, PredicateType: stmt.PredicateType, Predicate: stmt.Predicate}, true
}
