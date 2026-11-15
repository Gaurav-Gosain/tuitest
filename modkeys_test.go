package tuitest

import "testing"

// Alt of a named key uses the xterm modifier form. ESC followed by CSI A, the
// form Alt(Up) produced before, is not what any terminal sends, and a program
// reads it as Esc and then Up.
func TestAltOfNamedKeyUsesModifierForm(t *testing.T) {
	cases := []struct {
		name string
		got  Key
		want string
	}{
		{"Alt(Up)", Alt(Up), "\x1b[1;3A"},
		{"Alt(Left)", Alt(Left), "\x1b[1;3D"},
		{"Alt(Home)", Alt(Home), "\x1b[1;3H"},
		{"Alt(Delete)", Alt(Delete), "\x1b[3;3~"},
		{"Alt(PageDown)", Alt(PageDown), "\x1b[6;3~"},
		{"Alt(F1)", Alt(F1), "\x1b[1;3P"},
		{"Alt(F5)", Alt(F5), "\x1b[15;3~"},
		// Keys with no modifier form keep the ESC prefix.
		{"Alt('x')", Alt('x'), "\x1bx"},
		{"Alt(\"x\")", Alt("x"), "\x1bx"},
		{"Alt(Enter)", Alt(Enter), "\x1b\r"},
		{"Alt(Ctrl('b'))", Alt(Ctrl('b')), "\x1b\x02"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestModBuildsXtermModifierForm(t *testing.T) {
	cases := []struct {
		name string
		got  Key
		want string
	}{
		{"Mod(Up, Ctrl)", Mod(Up, ModCtrl), "\x1b[1;5A"},
		{"Mod(Right, Shift)", Mod(Right, ModShift), "\x1b[1;2C"},
		{"Mod(End, Ctrl|Shift)", Mod(End, ModCtrl|ModShift), "\x1b[1;6F"},
		{"Mod(Delete, Shift)", Mod(Delete, ModShift), "\x1b[3;2~"},
		{"Mod(F4, Ctrl|Alt)", Mod(F4, ModCtrl|ModAlt), "\x1b[1;7S"},
		{"Mod(F12, Shift)", Mod(F12, ModShift), "\x1b[24;2~"},
		{"Mod(Mod(Up, Ctrl), Shift)", Mod(Mod(Up, ModCtrl), ModShift), "\x1b[1;6A"},
		{"Alt(Mod(Down, Ctrl))", Alt(Mod(Down, ModCtrl)), "\x1b[1;7B"},
		{"Mod(Key(\"\\x1bOA\"), Ctrl)", Mod(Key("\x1bOA"), ModCtrl), "\x1b[1;5A"},
		{"Mod(Up, 0)", Mod(Up, 0), "\x1b[A"},
		// No modifier form: returned as it is.
		{"Mod(Enter, Ctrl)", Mod(Enter, ModCtrl), "\r"},
		{"Mod(Key(\"a\"), Ctrl)", Mod(Key("a"), ModCtrl), "a"},
		{"Mod(Key(\"\\x1b[R\"), Ctrl)", Mod(Key("\x1b[R"), ModCtrl), "\x1b[R"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}
