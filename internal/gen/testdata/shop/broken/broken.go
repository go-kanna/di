// Package broken holds a container whose dependency has a provider it cannot
// call.
package broken

import "github.com/go-kanna/di/internal/gen/testdata/shop/usecase"

//kanna:container
type auditing struct {
	audit *usecase.Audit
}
