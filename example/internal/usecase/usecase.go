// Package usecase holds what the shop does for its users.
package usecase

import (
	"context"
	"errors"
	"log/slog"

	"github.com/go-kanna/di/example/internal/user"
)

// ErrNoName reports a registration without a name.
var ErrNoName = errors.New("a user needs a name")

// RegisterUser registers a user.
type RegisterUser struct {
	repo user.Repository
	log  *slog.Logger
}

// NewRegisterUser provides a RegisterUser.
func NewRegisterUser(repo user.Repository, log *slog.Logger) *RegisterUser {
	return &RegisterUser{repo: repo, log: log}
}

// Do registers the user called name.
func (uc *RegisterUser) Do(ctx context.Context, name string) (user.User, error) {
	if name == "" {
		return user.User{}, ErrNoName
	}
	u := user.User{Name: name}
	if err := uc.repo.Save(ctx, u); err != nil {
		return user.User{}, err
	}
	uc.log.InfoContext(ctx, "registered", "name", name)
	return u, nil
}

// GetUser finds a user.
type GetUser struct {
	repo user.Repository
}

// NewGetUser provides a GetUser.
func NewGetUser(repo user.Repository) *GetUser {
	return &GetUser{repo: repo}
}

// Do finds the user called name.
func (uc *GetUser) Do(ctx context.Context, name string) (user.User, error) {
	return uc.repo.Find(ctx, name)
}
