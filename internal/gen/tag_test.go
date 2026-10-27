package gen_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/go-kanna/di/internal/gen"
)

// diTag is a struct tag holding s as its di tag.
func diTag(s string) reflect.StructTag {
	return reflect.StructTag(`json:"x" di:` + strconv.Quote(s))
}

func TestParseTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tag  reflect.StructTag
		want string
	}{
		{name: "absent", tag: `json:"name"`, want: "type"},
		{name: "empty", tag: diTag(""), want: "type"},
		{name: "skipped", tag: diTag("-"), want: "-"},
		{name: "argument", tag: diTag("arg"), want: "arg"},
		{name: "argument named", tag: diTag("arg=env"), want: "arg=env"},
		{name: "provider of the container's package", tag: diTag("with=openDB"), want: "with . openDB"},
		{
			name: "provider by package name",
			tag:  diTag("with=postgres.NewUserRepository"),
			want: "with postgres NewUserRepository",
		},
		{
			name: "provider by the end of an import path",
			tag:  diTag("with=infra/postgres.NewUserRepository"),
			want: "with infra/postgres NewUserRepository",
		},
		{
			name: "provider by an import path with dots",
			tag:  diTag("with=github.com/redis/go-redis/v9.NewClient"),
			want: "with github.com/redis/go-redis/v9 NewClient",
		},
		{
			// reflect skips a malformed value of another key.
			name: "another tag broken",
			tag:  `json:"a\d" di:"arg"`,
			want: "arg",
		},
		{name: "a key ending in di", tag: `xdi:"arg`, want: "type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := gen.ParseTag(tt.tag)
			if err != nil {
				t.Fatalf("parseTag(%s) error = %v", tt.tag, err)
			}
			if got != tt.want {
				t.Errorf("parseTag(%s) = %q, want %q", tt.tag, got, tt.want)
			}
		})
	}
}

func TestParseTag_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tag  reflect.StructTag
		want string
	}{
		{name: "unterminated", tag: `di:"arg`, want: "cannot read the di tag"},
		{name: "unknown", tag: diTag("args"), want: `unknown di tag "args"; write one of di:"-", di:"arg"`},
		{name: "more than one", tag: diTag("arg,with=openDB"), want: `di tag "arg,with=openDB" says more than one thing`},
		{name: "space", tag: diTag("with= openDB"), want: `di tag "with= openDB" contains a space`},
		{name: "skipped with a value", tag: diTag("-=x"), want: `di:"-" takes no value`},
		{name: "argument named nothing", tag: diTag("arg="), want: `di:"arg=" names no parameter`},
		{name: "argument named blank", tag: diTag("arg=_"), want: `di:"arg=_" names a parameter nothing can use`},
		{name: "argument named a number", tag: diTag("arg=1st"), want: `di:"arg=1st": 1st is not a Go identifier`},
		{name: "argument named a keyword", tag: diTag("arg=type"), want: "type is not a Go identifier"},
		{name: "provider without =", tag: diTag("with"), want: `di:"with" names no provider`},
		{name: "provider empty", tag: diTag("with="), want: `di:"with" names no provider`},
		{name: "provider without a name", tag: diTag("with=postgres."), want: "the function's name is missing"},
		{name: "provider without a package", tag: diTag("with=.NewUserRepository"), want: "the package is missing"},
		{name: "provider with a leading slash", tag: diTag("with=/postgres.New"), want: "/postgres is not an import path"},
		{name: "provider with a trailing slash", tag: diTag("with=postgres/.New"), want: "postgres/ is not an import path"},
		{name: "provider with two slashes", tag: diTag("with=infra//postgres.New"), want: "is not an import path"},
		{name: "provider named oddly", tag: diTag("with=postgres.New-Repo"), want: "New-Repo cannot name a function"},
		{name: "provider named blank", tag: diTag("with=_"), want: "_ cannot name a function"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := gen.ParseTag(tt.tag)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("parseTag(%s) error = %v, want it to mention %q", tt.tag, err, tt.want)
			}
		})
	}
}
