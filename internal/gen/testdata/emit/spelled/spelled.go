// Package spelled holds a container whose parameter is named in its tag after
// a package the constructor imports, which gives way to it.
package spelled

import "github.com/go-kanna/di/internal/gen/testdata/emit/shapes/config"

//go:generate go tool kanna-di

//kanna:container
type Spelled struct {
	cfg config.Config `di:"arg=config"`
}
