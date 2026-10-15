// Package vtgen generates terminal input that looks like something a program
// would actually emit, by grammar rather than by byte, for fuzzing a VT
// parser. A failing run reduces to a script of named sequences a person can
// read and retype.
//
// The generator lives in tuios.dev/vt/vtgen. This package forwards to it, so
// code that imports github.com/Gaurav-Gosain/tuitest/fuzz/vtgen keeps
// compiling. A seed gives a different script than it did in tuitest v0.1.0,
// because the generator changed when it moved.
//
// Deprecated: use tuios.dev/vt/vtgen.
package vtgen

import "tuios.dev/vt/vtgen"

// Seq is one step of a script: bytes to write, or a resize, with a description
// of what it is meant to be.
type Seq = vtgen.Seq

// Script is a run.
type Script = vtgen.Script

// Gen draws steps from a seeded source.
type Gen = vtgen.Gen

// New returns a generator seeded with seed. The same seed gives the same
// script.
func New(seed uint64) *Gen { return vtgen.New(seed) }

// FromBytes returns a generator that draws its choices from b, for a
// go test -fuzz corpus entry.
func FromBytes(b []byte) *Gen { return vtgen.FromBytes(b) }

// Shrink returns the smallest script it could reach that still fails. still
// replays a candidate from a clean emulator and reports whether the same thing
// still goes wrong.
func Shrink(s Script, still func(Script) bool) Script { return vtgen.Shrink(s, still) }
