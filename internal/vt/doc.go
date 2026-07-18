// These files are copied verbatim (non-test sources only) from tuios's
// internal/vt package so that tuitest interprets output with the exact same
// emulator tuios itself renders through, which maximizes fidelity when the
// program under test is tuios. The package has no tuios-internal imports; it
// depends only on github.com/charmbracelet/ultraviolet and
// github.com/charmbracelet/x/ansi. It is wrapped by internal/emu so the
// emulator choice stays behind an interface.

package vt
