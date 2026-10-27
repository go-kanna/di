module github.com/go-kanna/di/example

go 1.26.0

toolchain go1.26.9

// There is no require of github.com/go-kanna/di yet: it is unreleased, so this
// resolves through the repository's go.work. Once it carries a tag, running
// `go get -tool github.com/go-kanna/di/cmd/kanna-di` in your own module adds
// the require and the setup stands on its own.
tool github.com/go-kanna/di/cmd/kanna-di
