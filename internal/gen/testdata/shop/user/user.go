// Package user is the domain of the users of the shop.
package user

import "context"

// User is a user of the shop.
type User struct {
	Name string
}

// Repository keeps the users.
type Repository interface {
	Save(ctx context.Context, u User) error
	Find(ctx context.Context, name string) (User, error)
}
