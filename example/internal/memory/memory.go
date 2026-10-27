// Package memory is the infrastructure of the shop: a store kept in memory,
// standing in for a database, and the user.Repository on it.
package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/go-kanna/di/example/internal/config"
	"github.com/go-kanna/di/example/internal/user"
)

// Store holds what the shop keeps.
type Store struct {
	mu    sync.Mutex
	users map[string]user.User
}

// Open opens the store the settings name, and returns how to close it. A
// provider that opens something returns its cleanup this way, and kanna-di
// calls it from the cleanup of the constructor.
func Open(cfg config.Config) (*Store, func(), error) {
	if cfg.Store == "" {
		return nil, nil, errors.New("memory: no store named")
	}
	s := &Store{users: make(map[string]user.User)}
	return s, func() { fmt.Fprintln(os.Stdout, "closed the store", cfg.Store) }, nil
}

// UserRepository is the user.Repository on a Store.
type UserRepository struct {
	store *Store
}

// NewUserRepository provides a UserRepository. It returns the struct rather
// than the interface, and the container binds it to user.Repository.
func NewUserRepository(store *Store) *UserRepository {
	return &UserRepository{store: store}
}

// Save keeps u.
func (r *UserRepository) Save(_ context.Context, u user.User) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	r.store.users[u.Name] = u
	return nil
}

// Find returns the user called name.
func (r *UserRepository) Find(_ context.Context, name string) (user.User, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	u, ok := r.store.users[name]
	if !ok {
		return user.User{}, user.ErrNotFound
	}
	return u, nil
}
