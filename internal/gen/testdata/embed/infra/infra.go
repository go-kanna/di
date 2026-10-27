// Package infra is a container of another package than the one embedding it.
package infra

// DB stands in for a database handle.
type DB struct{}

// NewDB provides a *DB.
func NewDB() *DB { return &DB{} }

// Deps bundles what the application needs from the infrastructure.
//
//kanna:container returns=Deps
type Deps struct {
	DB *DB
}
