package config

import (
	"context"

	"github.com/nathanfredericks/transactions/internal/types"
)

type invocationCacheKey struct{}

type invocationCache struct {
	config     *types.Config
	parameters *types.Parameters
	secrets    *types.Secrets
}

func WithInvocationCache(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if getInvocationCache(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, invocationCacheKey{}, &invocationCache{})
}

func getInvocationCache(ctx context.Context) *invocationCache {
	if ctx == nil {
		return nil
	}
	cache, _ := ctx.Value(invocationCacheKey{}).(*invocationCache)
	return cache
}
