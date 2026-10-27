package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kanna/di/example/internal/user"
)

// testServer is the server, built here with a repository the test hands it
// instead of the one in memory. It embeds the container, so it takes what the
// server takes too, logs included, and has its routes.
//
//kanna:container
type testServer struct {
	*server

	_ user.Repository `di:"arg"`
}

// fakeRepo is a user.Repository the test can look into.
type fakeRepo map[string]user.User

func (r fakeRepo) Save(_ context.Context, u user.User) error {
	r[u.Name] = u
	return nil
}

func (r fakeRepo) Find(_ context.Context, name string) (user.User, error) {
	u, ok := r[name]
	if !ok {
		return user.User{}, user.ErrNotFound
	}
	return u, nil
}

func TestRegister(t *testing.T) {
	t.Parallel()

	repo := fakeRepo{}
	s := newTestServer(repo, io.Discard)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/users", strings.NewReader(`{"name":"alice"}`))
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body)
	}
	if _, ok := repo["alice"]; !ok {
		t.Error("the test's repository did not get alice")
	}
}
