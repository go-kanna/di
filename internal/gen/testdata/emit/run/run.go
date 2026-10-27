// Package run holds containers whose generated constructors the tests call, to
// see the providers run, fail, and clean up in the order they should.
package run

import "fmt"

//go:generate go tool kanna-di

// Log records what the providers did, and names the one that is to fail.
type Log struct {
	Lines []string
	Fail  string
}

func (l *Log) open(what string) error {
	if l.Fail == what {
		return fmt.Errorf("%s failed", what)
	}
	l.Lines = append(l.Lines, "open "+what)
	return nil
}

func (l *Log) closer(what string) func() {
	return func() { l.Lines = append(l.Lines, "close "+what) }
}

type (
	DB    struct{}
	Cache struct{ DB *DB }
	Queue struct{}
)

// OpenDB opens a DB, to be closed.
func OpenDB(log *Log) (*DB, func(), error) {
	if err := log.open("db"); err != nil {
		return nil, nil, err
	}
	return &DB{}, log.closer("db"), nil
}

// OpenCache opens a Cache on the DB, to be closed.
func OpenCache(log *Log, db *DB) (*Cache, func(), error) {
	if err := log.open("cache"); err != nil {
		return nil, nil, err
	}
	return &Cache{DB: db}, log.closer("cache"), nil
}

// OpenQueue opens a Queue, which needs no closing.
func OpenQueue(log *Log) (*Queue, error) {
	if err := log.open("queue"); err != nil {
		return nil, err
	}
	return &Queue{}, nil
}

// Service needs all of them.
type Service struct {
	DB    *DB
	Cache *Cache
	Queue *Queue
}

// NewService provides a Service.
func NewService(db *DB, cache *Cache, queue *Queue) *Service {
	return &Service{DB: db, Cache: cache, Queue: queue}
}

// App takes the log and builds the service.
//
//kanna:container must
type App struct {
	_ *Log `di:"arg"`

	Service *Service
}

// Stats is returned by value; its constructor fails with nothing to clean up.
//
//kanna:container returns=Stats must
type Stats struct {
	_ *Log `di:"arg"`

	Queue *Queue
}

// Starter is an interface a container is returned as.
type Starter interface {
	Start() string
}

// Runner is returned as a Starter, and never fails.
//
//kanna:container returns=Starter must
type Runner struct {
	Name Name `di:"arg"`
}

// Name names a Runner.
type Name string

// Start says who started.
func (r *Runner) Start() string { return "started " + string(r.Name) }
