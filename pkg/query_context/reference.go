package query_context

import (
	"context"

	"github.com/miekg/dns"
)

// ReferenceResolver resolves an internal lookup through the same executable
// chain that served the original query, without admission or policy. A policy
// uses it to learn about a name, not to answer the client, so the lookup is
// neither charged against quota nor rewritten by the user's own rules.
type ReferenceResolver func(ctx context.Context, request *dns.Msg) (*dns.Msg, error)

type referenceResolverKey struct{}

// WithReferenceResolver returns a context carrying r.
func WithReferenceResolver(ctx context.Context, r ReferenceResolver) context.Context {
	return context.WithValue(ctx, referenceResolverKey{}, r)
}

// ReferenceResolverFrom returns the resolver carried by ctx, or nil when the
// caller offers none, in which case a policy must skip the lookup.
func ReferenceResolverFrom(ctx context.Context) ReferenceResolver {
	r, _ := ctx.Value(referenceResolverKey{}).(ReferenceResolver)
	return r
}
