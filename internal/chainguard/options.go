package chainguard

type options struct {
	mode          authMode
	identity      string
	identityToken string
	concurrency   int
}

// Option configures New.
type Option func(*options)

// WithConcurrency caps the number of in-flight gRPC calls this
// client will make to console-api at any moment. Applied via a
// unary interceptor so every RPC method — current or future —
// participates. 0 or negative disables the limit.
func WithConcurrency(n int) Option {
	return func(o *options) { o.concurrency = n }
}

// WithIdentity selects assumable-identity auth: the workload-identity token
// is STS-exchanged for a Chainguard token using the given identity UIDP.
//
// token may be either a literal JWT or a filesystem path to a file holding
// one. File-based tokens are re-read on demand so Kubernetes service-account
// token rotation works without restarts.
//
// If WithIdentity is omitted, New falls back to the local chainctl token
// cache (run `chainctl auth login` beforehand).
func WithIdentity(identityUIDP, token string) Option {
	return func(o *options) {
		o.mode = authIdentity
		o.identity = identityUIDP
		o.identityToken = token
	}
}
