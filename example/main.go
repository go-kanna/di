// Command example wires a small shop with kanna-di and sends it a few requests.
//
// Regenerate with:
//
//	go generate ./...
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/go-kanna/di/example/internal/handler"
	"github.com/go-kanna/di/example/internal/mail"
	"github.com/go-kanna/di/example/internal/user"
)

//go:generate go tool kanna-di

// server serves the shop's HTTP API. It is the struct the composition root
// has anyway: what the program is made of, and its routes. The fields kanna-di
// fills are its dependencies; the tags say what only this root decides.
//
//kanna:container must
type server struct {
	// The constructor takes where the mail goes, named out.
	_ io.Writer `di:"arg=out"`

	// The domain's interfaces are served by the infrastructure's
	// implementations, which these functions return as structs.
	_ user.Repository `di:"with=memory.NewUserRepository"`
	_ mail.Sender     `di:"with=console.NewSender"`

	users *handler.User
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users", s.users.Register)
	mux.HandleFunc("GET /users/{name}", s.users.Get)
	return mux
}

func main() {
	// The must variant panics where newServer would return an error; the
	// cleanup closes what the providers opened.
	s, cleanup := mustNewServer(os.Stdout)
	defer cleanup()

	routes := s.routes()
	for _, req := range []struct{ method, path, body string }{
		{"POST", "/users", `{"name":"alice"}`},
		{"GET", "/users/alice", ""},
		{"GET", "/users/bob", ""},
		{"POST", "/users", `{"name":""}`},
	} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(context.Background(), req.method, req.path, strings.NewReader(req.body))
		routes.ServeHTTP(rec, r)
		fmt.Fprintf(os.Stdout, "%s %s -> %d %s", req.method, req.path, rec.Code, rec.Body)
	}
}
