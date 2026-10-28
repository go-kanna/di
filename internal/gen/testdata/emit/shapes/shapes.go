// Package shapes holds containers whose generated constructors show the shapes
// the generator writes: a field of an embedded struct or of a blank one, a
// container built in place, variables kept clear of the names the package
// declares, and a parameter named as the package its type comes from.
package shapes

import "github.com/go-kanna/di/internal/gen/testdata/emit/shapes/config"

//go:generate go tool kanna-di

type (
	DB    struct{}
	Users struct{}
	Type  struct{}
)

func newDB() *DB { return &DB{} }

func newType() *Type { return &Type{} }

// newUsers names neither of its parameters, so the values passed to it are
// named after their types: db, and type, which being a keyword gives way.
func newUsers(*DB, *Type) *Users { return &Users{} }

// users is a function of the package, which a variable must not hide.
func users() {}

// Deps is a struct a container takes and draws from.
type Deps struct {
	DB *DB
}

// base is built in place by wrapped.
//
//kanna:container
type base struct {
	users *Users
}

// wrapped takes Deps, whose DB serves newUsers, and is base.
//
//kanna:container
type wrapped struct {
	Deps `di:"arg"`

	*base
}

// shared needs base twice, which is then built once, into a variable.
//
//kanna:container
type shared struct {
	*base

	again *base
}

// drawn draws its users from a *Deps it chooses but does not keep.
//
//kanna:container
type drawn struct {
	_ *Deps `di:"with=newDeps"`

	users *Users
}

func newDeps() *Deps { return &Deps{DB: newDB()} }

// empty has nothing to build.
//
//kanna:container
type empty struct{}

// configured takes a config.Config, whose parameter is named config after
// its type: the import gives way to it.
//
//kanna:container
type configured struct {
	cfg config.Config `di:"arg"`
}

var _ = users
