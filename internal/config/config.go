package config

import (
	"context"
	"github.com/nathanfredericks/transactions/internal/types"
)

func GetConfig(ctx context.Context) (*types.Config, error) {
	s, e := Load(ctx)
	if e != nil {
		return nil, e
	}
	return &types.Config{Email: s.Email, Webhook: s.Webhook}, nil
}
