package chainguard

import (
	"context"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc"
)

// rateLimitInterceptor returns a gRPC unary interceptor that gates
// every RPC on a shared semaphore. Because it sits on the gRPC
// dialer, it covers every current and future RPC method the client
// makes without any per-call bookkeeping.
func rateLimitInterceptor(concurrency int) grpc.UnaryClientInterceptor {
	sem := semaphore.NewWeighted(int64(concurrency))
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if err := sem.Acquire(ctx, 1); err != nil {
			return err
		}
		defer sem.Release(1)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
