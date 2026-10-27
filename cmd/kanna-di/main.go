// Command kanna-di writes the constructors of structs marked //kanna:container.
package main

import (
	"os"

	"github.com/go-kanna/di/internal/gen"
)

func main() {
	os.Exit(gen.Command().Main(os.Args[1:]))
}
