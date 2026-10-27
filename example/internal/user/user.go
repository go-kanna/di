// Package user is the domain of the shop's users.
package user

import (
	"context"
	"errors"
)

// ErrNotFound reports a user who is not there.
var ErrNotFound = errors.New("user not found")

// User is a user of the shop.
type User struct {
	Name string `json:"name"`
}

// Repository keeps the users. The domain declares it, the infrastructure
// implements it, and the composition root says which implementation serves.
type Repository interface {
	Save(ctx context.Context, u User) error
	Find(ctx context.Context, name string) (User, error)
}
