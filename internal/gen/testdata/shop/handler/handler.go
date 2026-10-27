// Package handler serves the use cases of the shop over HTTP.
package handler

import (
	"net/http"

	"github.com/go-kanna/di/internal/gen/testdata/shop/usecase"
)

// User serves the use cases about users.
type User struct {
	registerUser *usecase.RegisterUser
	getUser      *usecase.GetUser
}

// NewUser provides a User.
func NewUser(registerUser *usecase.RegisterUser, getUser *usecase.GetUser) *User {
	return &User{registerUser: registerUser, getUser: getUser}
}

// Register registers a user.
func (h *User) Register(http.ResponseWriter, *http.Request) {}

// Get finds a user.
func (h *User) Get(http.ResponseWriter, *http.Request) {}
