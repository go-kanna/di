// Package handler serves the use cases of the shop over HTTP.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-kanna/di/example/internal/usecase"
	"github.com/go-kanna/di/example/internal/user"
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

// Register registers the user the request body names.
func (h *User) Register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	u, err := h.registerUser.Do(r.Context(), body.Name)
	if errors.Is(err, usecase.ErrNoName) {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeUser(w, http.StatusCreated, u)
}

// Get finds the user the path names.
func (h *User) Get(w http.ResponseWriter, r *http.Request) {
	u, err := h.getUser.Do(r.Context(), r.PathValue("name"))
	if errors.Is(err, user.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeUser(w, http.StatusOK, u)
}

func writeUser(w http.ResponseWriter, status int, u user.User) {
	body, err := json.Marshal(u)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
