// Package usecase holds what the shop does for its users.
package usecase

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-kanna/di/example/internal/mail"
	"github.com/go-kanna/di/example/internal/user"
)

// ErrNoName reports a registration without a name.
var ErrNoName = errors.New("a user needs a name")

// RegisterUser registers a user.
type RegisterUser struct {
	repo   user.Repository
	mailer mail.Sender
}

// NewRegisterUser provides a RegisterUser.
func NewRegisterUser(repo user.Repository, mailer mail.Sender) *RegisterUser {
	return &RegisterUser{repo: repo, mailer: mailer}
}

// Do registers the user called name and sends them a welcome mail.
func (uc *RegisterUser) Do(ctx context.Context, name string) (user.User, error) {
	if name == "" {
		return user.User{}, ErrNoName
	}
	u := user.User{Name: name}
	if err := uc.repo.Save(ctx, u); err != nil {
		return user.User{}, fmt.Errorf("register %s: %w", name, err)
	}
	if err := uc.mailer.Send(ctx, mail.Message{To: name, Subject: "Welcome"}); err != nil {
		return user.User{}, fmt.Errorf("welcome %s: %w", name, err)
	}
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
	u, err := uc.repo.Find(ctx, name)
	if err != nil {
		return user.User{}, fmt.Errorf("get %s: %w", name, err)
	}
	return u, nil
}
