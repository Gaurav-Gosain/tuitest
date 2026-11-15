package tape

import "github.com/Gaurav-Gosain/tuitest/internal/keyenc"

// The key table lives in internal/keyenc, which the root package uses to send
// keys too, so the recorder, the player and SendKeys agree on every key. These
// names keep the protocol files short.

type keyDef = keyenc.KeyDef

const (
	formLetter = keyenc.FormLetter
	formTilde  = keyenc.FormTilde
	formSS3    = keyenc.FormSS3
)

var (
	keyDefs     = keyenc.KeyDefs
	keyByName   = keyenc.KeyByName
	keyByLetter = keyenc.KeyByLetter
	keyByTilde  = keyenc.KeyByTilde
	keyBySS3    = keyenc.KeyBySS3
	keyByKitty  = keyenc.KeyByKitty
	modAliases  = keyenc.ModAliases
)

const (
	modShift    = keyenc.ModShift
	modAlt      = keyenc.ModAlt
	modCtrl     = keyenc.ModCtrl
	modSuper    = keyenc.ModSuper
	modHyper    = keyenc.ModHyper
	modMeta     = keyenc.ModMeta
	modCapsLock = keyenc.ModCapsLock
	modNumLock  = keyenc.ModNumLock
)

func modString(mask int) string { return keyenc.ModString(mask) }

func splitToken(tok string) (int, string, bool) { return keyenc.SplitToken(tok) }

func singleRune(s string) (rune, bool) { return keyenc.SingleRune(s) }
