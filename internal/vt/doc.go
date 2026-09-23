// These files are copied from tuios's internal/vt package so that tuitest
// interprets output with the same emulator tuios itself renders through, which
// maximizes fidelity when the program under test is tuios. The package has no
// tuios-internal imports; it depends only on github.com/charmbracelet/ultraviolet
// and github.com/charmbracelet/x/ansi. It is wrapped by internal/emu so the
// emulator choice stays behind an interface.
//
// VENDOR.md says what is copied, which files belong to tuitest (this one and
// every tuitest_* file), and which fixes this copy carries that tuios does not
// have yet. UPSTREAM records the tuios commit it was taken from.

package vt
