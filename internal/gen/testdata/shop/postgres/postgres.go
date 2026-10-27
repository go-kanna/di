// Package postgres keeps the users of the shop in PostgreSQL.
package postgres

import (
	"context"
	"database/sql"

	"github.com/go-kanna/di/internal/gen/testdata/shop/user"
)

// UserRepository is a user.Repository on a database.
type UserRepository struct {
	db *sql.DB
}

// NewUserRepository provides a UserRepository.
func NewUserRepository(db *sql.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) Save(context.Context, user.User) error { return nil }

func (r *UserRepository) Find(context.Context, string) (user.User, error) { return user.User{}, nil }
