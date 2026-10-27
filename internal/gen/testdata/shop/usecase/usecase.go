// Package usecase holds what the shop does for its users.
package usecase

import (
	"log/slog"

	"github.com/go-kanna/di/internal/gen/testdata/shop/user"
)

// RegisterUser registers a user.
type RegisterUser struct {
	repo user.Repository
	log  *slog.Logger
}

// NewRegisterUser provides a RegisterUser.
func NewRegisterUser(repo user.Repository, log *slog.Logger) *RegisterUser {
	return &RegisterUser{repo: repo, log: log}
}

// GetUser finds a user.
type GetUser struct {
	repo user.Repository
}

// NewGetUser provides a GetUser.
func NewGetUser(repo user.Repository) *GetUser {
	return &GetUser{repo: repo}
}

// newAudit returns an Audit, but nothing outside the package can call it.
func newAudit() *Audit { return &Audit{} }

// Audit records what users do.
type Audit struct{}
