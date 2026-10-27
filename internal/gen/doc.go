// Package gen writes the constructors of structs marked //kanna:container.
// cmd/kanna-di runs it.
//
// Each field of a container is filled from a provider: a function returning the
// field's type, found next to the type or next to the container, or named with
// di:"with=...". The constructor calls the providers in order, in plain Go, so
// nothing is looked up at run time; a field nothing provides stops generation
// instead.
package gen
