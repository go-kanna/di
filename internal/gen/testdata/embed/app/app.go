// Package app embeds a container of another package, which is built by its own
// constructor rather than in place.
package app

import "github.com/go-kanna/di/internal/gen/testdata/embed/infra"

// Users stands in for a service.
type Users struct{}

func newUsers(db *infra.DB) *Users { return &Users{} }

//kanna:container
type App struct {
	infra.Deps

	Users *Users
}
