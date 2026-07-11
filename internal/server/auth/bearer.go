package auth

import (
	"context"
	"errors"
)

type BearerChain []BearerAuthenticator

func (chain BearerChain) AuthenticateBearer(ctx context.Context, token string) (Principal, error) {
	for _, authenticator := range chain {
		if authenticator == nil {
			continue
		}
		principal, err := authenticator.AuthenticateBearer(ctx, token)
		if err == nil {
			return principal, nil
		}
		if !errors.Is(err, ErrInvalidCredential) {
			return Principal{}, err
		}
	}
	return Principal{}, ErrInvalidCredential
}
