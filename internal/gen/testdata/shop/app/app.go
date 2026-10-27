// Package app is the composition root of the shop.
package app

import (
	"database/sql"
	"log/slog"
	"os"

	"github.com/go-kanna/di/internal/gen/testdata/shop/config"
	"github.com/go-kanna/di/internal/gen/testdata/shop/handler"
	"github.com/go-kanna/di/internal/gen/testdata/shop/user"
)

// server serves the shop.
//
//kanna:container
type server struct {
	_ user.Repository `di:"with=postgres.NewUserRepository"`

	users *handler.User
}

// testServer takes the repository instead, so that it needs no database.
//
//kanna:container
type testServer struct {
	_ user.Repository `di:"arg"`

	users *handler.User
}

func openDB(cfg config.Config) (*sql.DB, func(), error) {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, nil, err
	}
	return db, func() { db.Close() }, nil
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}
