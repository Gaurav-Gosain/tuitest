package keyenc

import "testing"

func TestReencode(t *testing.T) {
	const (
		disambiguate = KittyDisambiguate
		tuios        = KittyDisambiguate | KittyAlternates | KittyAllKeys | KittyText // CSI = 29 ; 1 u
	)
	cases := []struct {
		name string
		in   string
		m    Modes
		want string
	}{
		// Legacy: unchanged, DECCKM moves the unmodified cursor keys to SS3.
		{"legacy Ctrl+b", "\x02", Modes{}, "\x02"},
		{"legacy Up", "\x1b[A", Modes{}, "\x1b[A"},
		{"legacy Up spelled CSI 1 A", "\x1b[1A", Modes{}, "\x1b[1A"},
		{"DECCKM Up", "\x1b[A", Modes{AppCursor: true}, "\x1bOA"},
		{"DECCKM Ctrl+Up", "\x1b[1;5A", Modes{AppCursor: true}, "\x1b[1;5A"},
		{"DECCKM Home", "\x1b[H", Modes{AppCursor: true}, "\x1bOH"},
		{"text passes", "hi", Modes{}, "hi"},
		{"not a key passes", "\x1b[?1u", Modes{}, "\x1b[?1u"},
		{"lone Esc", "\x1b", Modes{}, "\x1b"},

		// Kitty, disambiguate only.
		{"kitty Ctrl+b", "\x02", Modes{KittyFlags: disambiguate}, "\x1b[98;5u"},
		{"kitty Alt+x", "\x1bx", Modes{KittyFlags: disambiguate}, "\x1b[120;3u"},
		{"kitty Esc", "\x1b", Modes{KittyFlags: disambiguate}, "\x1b[27u"},
		{"kitty Enter stays", "\r", Modes{KittyFlags: disambiguate}, "\r"},
		{"kitty Tab stays", "\t", Modes{KittyFlags: disambiguate}, "\t"},
		{"kitty Backspace stays", "\x7f", Modes{KittyFlags: disambiguate}, "\x7f"},
		{"kitty Alt+Enter", "\x1b\r", Modes{KittyFlags: disambiguate}, "\x1b[13;3u"},
		{"kitty Alt+Esc", "\x1b\x1b", Modes{KittyFlags: disambiguate}, "\x1b[27;3u"},
		{"legacy Alt+Esc", "\x1b\x1b", Modes{}, "\x1b\x1b"},
		{"kitty Up stays", "\x1b[A", Modes{KittyFlags: disambiguate}, "\x1b[A"},
		{"kitty Up DECCKM", "\x1b[A", Modes{KittyFlags: disambiguate, AppCursor: true}, "\x1bOA"},
		{"kitty Ctrl+Up", "\x1b[1;5A", Modes{KittyFlags: disambiguate}, "\x1b[1;5A"},
		{"kitty Shift+Delete", "\x1b[3;2~", Modes{KittyFlags: disambiguate}, "\x1b[3;2~"},
		{"kitty Ctrl+F3", "\x1b[1;5R", Modes{KittyFlags: disambiguate}, "\x1b[13;5~"},
		{"kitty text stays", "hi", Modes{KittyFlags: disambiguate}, "hi"},

		// Kitty with every key reported, as tuios sets it in window mode.
		{"all Enter", "\r", Modes{KittyFlags: tuios}, "\x1b[13u"},
		{"all Tab", "\t", Modes{KittyFlags: tuios}, "\x1b[9u"},
		{"all Ctrl+b", "\x02", Modes{KittyFlags: tuios}, "\x1b[98;5u"},
		{"all Up", "\x1b[A", Modes{KittyFlags: tuios}, "\x1b[A"},
		{"all F1", "\x1bOP", Modes{KittyFlags: tuios}, "\x1b[P"},
		{"all F3", "\x1bOR", Modes{KittyFlags: tuios}, "\x1b[13~"},
		{"all text", "a", Modes{KittyFlags: tuios}, "\x1b[97;;97u"},
		{"all shifted text", "A", Modes{KittyFlags: tuios}, "\x1b[97:65;2;65u"},
		{"all text without text flag", "a", Modes{KittyFlags: KittyDisambiguate | KittyAllKeys}, "\x1b[97u"},

		// modifyOtherKeys.
		{"mok2 Ctrl+b", "\x02", Modes{ModifyOtherKeys: 2}, "\x1b[27;5;98~"},
		{"mok2 Enter", "\r", Modes{ModifyOtherKeys: 2}, "\r"},
		{"mok2 Ctrl+Up", "\x1b[1;5A", Modes{ModifyOtherKeys: 2}, "\x1b[1;5A"},
		{"mok1 Ctrl+b", "\x02", Modes{ModifyOtherKeys: 1}, "\x02"},
	}
	for _, c := range cases {
		if got := string(Reencode([]byte(c.in), c.m)); got != c.want {
			t.Errorf("%s: Reencode(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
