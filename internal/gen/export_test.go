package gen

import (
	"fmt"
	"reflect"
)

// ParseTag is parseTag spelled out: "type", "-", "arg", "arg=<param>", or
// "with <pkg> <fn>", with "." as the package for a function of the
// container's package.
func ParseTag(st reflect.StructTag) (string, error) {
	t, err := parseTag(st)
	if err != nil {
		return "", err
	}

	switch t.source {
	case fromType:
		return "type", nil
	case fromNowhere:
		return "-", nil
	case fromArg:
		if t.param == "" {
			return "arg", nil
		}
		return "arg=" + t.param, nil
	case fromRef:
		pkg := t.ref.pkg
		if pkg == "" {
			pkg = "."
		}
		return "with " + pkg + " " + t.ref.fn, nil
	default:
		return "", fmt.Errorf("source %d", t.source)
	}
}
