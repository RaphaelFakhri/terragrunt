package hclparse

import (
	"context"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gruntwork-io/terragrunt/internal/vfs"
	pkghclparse "github.com/gruntwork-io/terragrunt/pkg/config/hclparse"
)

// Stage is a step of a config parse whose result the Store memoizes.
type Stage uint8

const (
	// StageAST is a file parsed to its syntax tree. It reads nothing but the file, so its [Key] has no Context.
	StageAST Stage = iota + 1
	// StageQueue is a file decoded with no dependency outputs in scope, once per [Key.Context].
	StageQueue
)

// Key identifies one memoized result.
type Key struct {
	// Path is the cleaned path of the result's file.
	Path string
	// Context is a digest of every input beyond the file that the stage reads, empty for a stage that reads none.
	Context string
	Stage   Stage
}

// Store memoizes the stages of config parses for the life of a process, so a file is parsed once however many
// units include it and a unit is evaluated once however many discovery passes reach it. The zero value is ready
// to use, and every method is safe for concurrent use.
//
// Each key has a promise. The first caller of a key runs its body while later callers wait for the outcome.
// The Store keeps a result only when the body returns no error, so the next caller runs a failed body again. When
// a body fails because its caller's context ended, the waiters run the body again under their own contexts.
//
// A body must not resolve its own key, directly or through the bodies it calls, or it waits on itself forever.
// The config rewrite puts the chain of files a parse went through into [Key.Context], so every step of a
// recursive parse has its own key.
//
// The Store keeps every successful result until [Store.Invalidate] drops it or, for a syntax tree, the file
// changes. Its memory grows with the number of distinct files and contexts a process parses.
type Store struct {
	entries map[Key]*entry
	mu      sync.Mutex
	hits    atomic.Uint64
	misses  atomic.Uint64
}

// Resolved is the outcome of [Store.Resolve].
type Resolved[T any] struct {
	// Value is what the body returned.
	Value T
	// Reads is every path that the body, or a body it resolved, reported through [NoteReads], in lexical order.
	Reads []string
	// Hit is true when another call ran the body.
	Hit bool
}

// Stats counts the outcomes of [Store.Resolve].
type Stats struct {
	// Hits counts the calls that received the result of another call's body.
	Hits uint64
	// Misses counts the calls that ran a body.
	Misses uint64
}

// entry is the promise of one key.
type entry struct {
	// done closes once the body has returned.
	done chan struct{}
	val  any
	err  error
	// reads is the paths the body reported through [NoteReads]. Every caller that shares the result gets them.
	reads map[string]struct{}
	// files is reads plus the path of every file the body parsed through the Store. [Store.Invalidate] matches
	// against it.
	files map[string]struct{}
	mu    sync.Mutex
	// abandoned is true when the body failed after its caller's context ended.
	abandoned bool
}

// flightKey is the context key of the entry whose body is running.
type flightKey struct{}

// Resolve returns the value of k, and runs fn to produce it when no earlier call has. Concurrent calls for k
// share one run of fn. The Store keeps the value only when fn returns nil.
//
// fn receives a context with ctx's values that also identifies the run. Every [Store.AST], [NoteReads], and
// nested Resolve call under that context adds its reads to the entry.
//
// Returns fn's error, with fn's value, when the run that this call joined failed. When that run failed because
// the context of the call that ran it ended, this call runs fn again instead. Returns ctx.Err() with no value
// when ctx ends while the call waits.
//
// Panics with fn's value when fn panics, on the call that ran fn only. Calls waiting on that run receive a
// [FlightAbortedError].
func (s *Store) Resolve[T any](
	ctx context.Context,
	k Key,
	fn func(context.Context) (T, error),
) (Resolved[T], error) {
	for {
		e, lead := s.join(k)
		if lead {
			s.misses.Add(1)

			return s.run(ctx, k, e, fn)
		}

		select {
		case <-e.done:
			if e.abandoned && ctx.Err() == nil {
				continue
			}

			s.hits.Add(1)
			e.propagate(ctx)

			return resolved[T](e, true), e.err
		case <-ctx.Done():
			var zero Resolved[T]

			return zero, ctx.Err()
		}
	}
}

// AST returns the syntax tree of the file at path. The first caller parses it with parser, and every later caller
// gets the tree bound to its own parser. info is the file's current metadata. AST parses the file again when its
// size or modification time differs from the metadata of the retained tree.
//
// Returns the parse error when the file fails to parse.
func (s *Store) AST(
	ctx context.Context,
	fsys vfs.FS,
	path string,
	info fs.FileInfo,
	parser *pkghclparse.Parser,
) (*pkghclparse.File, error) {
	path = filepath.Clean(path)
	k := Key{Path: path, Stage: StageAST}

	s.dropStale(k, info)

	res, err := s.Resolve(ctx, k, func(context.Context) (*astFile, error) {
		file, err := parser.ParseFromFile(fsys, path)
		if err != nil {
			return nil, err
		}

		return &astFile{file: file, modTime: info.ModTime(), size: info.Size()}, nil
	})
	if err != nil {
		return nil, err
	}

	if !res.Hit {
		return res.Value.file, nil
	}

	return res.Value.file.Rebind(parser), nil
}

// Invalidate drops the retained results of the file at path, and every result whose run parsed that file through
// [Store.AST] or reported reading it through [NoteReads]. It drops a run in progress too, and the calls already
// waiting on that run still receive its result.
func (s *Store) Invalidate(path string) {
	path = filepath.Clean(path)

	s.mu.Lock()
	defer s.mu.Unlock()

	for k, e := range s.entries {
		if e.read(path) {
			delete(s.entries, k)
		}
	}
}

// Stats returns the counts so far.
func (s *Store) Stats() Stats {
	return Stats{Hits: s.hits.Load(), Misses: s.misses.Load()}
}

// NoteReads adds paths to the reads of the run that ctx belongs to. It does nothing when ctx belongs to no run.
func NoteReads(ctx context.Context, paths ...string) {
	e, ok := ctx.Value(flightKey{}).(*entry)
	if !ok {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	for _, path := range paths {
		e.reads[path] = struct{}{}
		e.files[path] = struct{}{}
	}
}

// astFile is a [StageAST] value with the metadata of the file it was parsed from.
type astFile struct {
	file    *pkghclparse.File
	modTime time.Time
	size    int64
}

// join returns the entry of k and whether the caller has to run its body.
func (s *Store) join(k Key) (*entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.entries[k]; ok {
		return e, false
	}

	if s.entries == nil {
		s.entries = make(map[Key]*entry)
	}

	e := &entry{
		done:  make(chan struct{}),
		reads: make(map[string]struct{}),
		files: map[string]struct{}{k.Path: {}},
	}
	s.entries[k] = e

	return e, true
}

// run executes fn for e and publishes its outcome.
func (s *Store) run[T any](
	ctx context.Context,
	k Key,
	e *entry,
	fn func(context.Context) (T, error),
) (Resolved[T], error) {
	returned := false

	defer func() {
		if returned {
			return
		}

		r := recover()

		var zero T

		s.finish(k, e, zero, FlightAbortedError{Key: k, Panic: r}, false)

		if r != nil {
			panic(r)
		}
	}()

	val, err := fn(context.WithValue(ctx, flightKey{}, e))
	returned = true

	s.finish(k, e, val, err, err != nil && ctx.Err() != nil)
	e.propagate(ctx)

	return resolved[T](e, false), err
}

// finish records the outcome of e's body, drops e from the map when the body failed, and wakes the waiters.
func (s *Store) finish(k Key, e *entry, val any, err error, abandoned bool) {
	s.mu.Lock()

	e.val = val
	e.err = err
	e.abandoned = abandoned

	if err != nil && s.entries[k] == e {
		delete(s.entries, k)
	}

	s.mu.Unlock()

	close(e.done)
}

// dropStale drops the retained tree of k when info differs from the metadata of the file it was parsed from. It
// leaves a run in progress alone.
func (s *Store) dropStale(k Key, info fs.FileInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[k]
	if !ok {
		return
	}

	select {
	case <-e.done:
	default:
		return
	}

	parsed, ok := e.val.(*astFile)
	if !ok {
		return
	}

	if parsed.size != info.Size() || !parsed.modTime.Equal(info.ModTime()) {
		delete(s.entries, k)
	}
}

// resolved returns e's outcome as a [Resolved]. The value is the zero value when the body returned none.
func resolved[T any](e *entry, hit bool) Resolved[T] {
	val, _ := e.val.(T)

	e.mu.Lock()
	defer e.mu.Unlock()

	return Resolved[T]{Value: val, Reads: slices.Sorted(maps.Keys(e.reads)), Hit: hit}
}

// propagate adds e's reads and files to the run that ctx belongs to, if ctx belongs to one.
func (e *entry) propagate(ctx context.Context) {
	parent, ok := ctx.Value(flightKey{}).(*entry)
	if !ok || parent == e {
		return
	}

	e.mu.Lock()
	reads := maps.Clone(e.reads)
	files := maps.Clone(e.files)
	e.mu.Unlock()

	parent.mu.Lock()
	defer parent.mu.Unlock()

	maps.Copy(parent.reads, reads)
	maps.Copy(parent.files, files)
}

// read reports whether e's body parsed or read path.
func (e *entry) read(path string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	_, ok := e.files[path]

	return ok
}
