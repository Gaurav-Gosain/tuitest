#!/usr/bin/env python3
"""Write the adversarial half of the VT corpus."""
import os, sys

out = sys.argv[1]
os.makedirs(out, exist_ok=True)
E = "\x1b"

# A recognisable filler so misplaced text is obvious in a diff.
def fill(n=24, w=80):
    s = ""
    for r in range(n):
        s += ("%02d" % r) + ("abcdefghij" * 8)[: w - 2] + "\r\n" * (r < n - 1)
    return s

cases = {
    # --- scroll regions -----------------------------------------------------
    "scroll_region_basic": fill() + f"{E}[5;10r{E}[10;1H" + "\n" * 5 + "X",
    "scroll_region_ri": fill() + f"{E}[5;10r{E}[5;1H" + f"{E}M" * 3 + "Y",
    "scroll_region_su_sd": fill() + f"{E}[3;8r{E}[4S{E}[2T",
    "scroll_region_reset": fill() + f"{E}[5;10r{E}[r{E}[24;1H\n\nZ",
    "scroll_region_full_su": fill() + f"{E}[3S",
    # left/right margins (DECLRMM). Real programs almost never use these, but
    # a wrong answer here means the margin code is wrong generally.
    "scroll_region_lr": fill() + f"{E}[?69h{E}[20;40s{E}[5;25H{E}[3S",
    # --- insert / delete ----------------------------------------------------
    "il_dl": fill() + f"{E}[5;1H{E}[3L{E}[10;1H{E}[2M",
    "il_dl_in_region": fill() + f"{E}[5;15r{E}[8;1H{E}[3L{E}[12;1H{E}[2M",
    "il_outside_region": fill() + f"{E}[5;15r{E}[2;1H{E}[5L",
    "ich_dch": fill() + f"{E}[3;5H{E}[4@{E}[6;5H{E}[7P",
    "ich_dch_huge": fill() + f"{E}[3;5H{E}[999@{E}[6;5H{E}[999P",
    "ech": fill() + f"{E}[4;10H{E}[15X",
    "ech_huge": fill() + f"{E}[4;10H{E}[9999X",
    # --- erase --------------------------------------------------------------
    "el_modes": fill() + f"{E}[3;40H{E}[K{E}[5;40H{E}[1K{E}[7;40H{E}[2K",
    "ed_modes": fill() + f"{E}[12;40H{E}[J",
    "ed_1": fill() + f"{E}[12;40H{E}[1J",
    "ed_2": fill() + f"{E}[12;40H{E}[2J",
    "ed_3": fill() + f"{E}[12;40H{E}[3J",
    "erase_keeps_bg": fill() + f"{E}[41m{E}[3;1H{E}[2K{E}[5;1H{E}[J",
    "decsed_decsel": fill() + f"{E}[3;10H{E}[?0K{E}[6;10H{E}[?1J",
    # --- tabs ---------------------------------------------------------------
    "tabs_default": f"{E}[2J{E}[Ha\tb\tc\td\te",
    "tabs_set_clear": f"{E}[2J{E}[H{E}[3G{E}H{E}[10G{E}H{E}[1G" + "x\ty\tz",
    "tabs_cbt": f"{E}[2J{E}[H\t\t\tQ{E}[2ZR",
    "tabs_tbc3": f"{E}[2J{E}[H{E}[3ga\tb\tc",
    "tabs_cht": f"{E}[2J{E}[H{E}[3IX",
    # --- origin mode / cursor ----------------------------------------------
    "origin_mode": fill() + f"{E}[5;10r{E}[?6h{E}[1;1HA{E}[3;3HB{E}[99;1HC",
    "origin_mode_off": fill() + f"{E}[5;10r{E}[?6h{E}[?6l{E}[1;1HA",
    "cursor_clamp": fill() + f"{E}[999;999HZ{E}[0;0HA",
    "cup_missing_params": fill() + f"{E}[;5HA{E}[7;HB{E}[HC",
    "cursor_moves": fill() + f"{E}[10;10H{E}[3A{E}[4C{E}[2B{E}[5DX{E}[dY{E}[5`Z",
    "hpr_vpr": fill() + f"{E}[1;1H{E}[10a{E}[5eX{E}[3jY{E}[2kZ",
    # --- autowrap / reverse wrap -------------------------------------------
    "autowrap_on": f"{E}[2J{E}[H" + "W" * 100,
    "autowrap_off": f"{E}[2J{E}[H{E}[?7l" + "W" * 100 + "E",
    "wrap_then_bs": f"{E}[2J{E}[H" + "W" * 80 + f"\b\bX",
    "reverse_wrap": f"{E}[2J{E}[H{E}[?45h{E}[2;1H" + "\b\b" + "R",
    "pending_wrap_cr": f"{E}[2J{E}[H" + "W" * 80 + "\rX",
    # --- character sets -----------------------------------------------------
    "charset_dec_special": f"{E}[2J{E}[H{E}(0lqqqk\r\nx  x\r\nmqqqj{E}(B done",
    "charset_g1_so": f"{E}[2J{E}[H{E})0\x0elqk\x0f ascii",
    "charset_scs_g2": f"{E}[2J{E}[H{E}*0{E}Nl normal",
    # --- save / restore cursor ---------------------------------------------
    "decsc_decrc": fill() + f"{E}[5;5H{E}7{E}[20;40HX{E}8Y",
    "sco_save_restore": fill() + f"{E}[5;5H{E}[s{E}[20;40HX{E}[uY",
    "decsc_keeps_sgr": f"{E}[2J{E}[H{E}[1;31m{E}7{E}[0mplain{E}8styled",
    "decsc_origin": fill() + f"{E}[5;10r{E}[?6h{E}[2;2H{E}7{E}[?6l{E}[1;1H{E}8X",
    # --- alternate screen ---------------------------------------------------
    "alt_1049": fill() + f"{E}[?1049h{E}[2J{E}[HALT SCREEN{E}[3;1Hsecond",
    "alt_1049_back": fill() + f"{E}[?1049h{E}[2J{E}[HALT{E}[?1049l",
    "alt_1047": fill() + f"{E}[?1047h{E}[2J{E}[HALT47",
    "alt_1047_back": fill() + f"{E}[?1047h{E}[2J{E}[HALT47{E}[?1047l",
    "alt_1049_cursor": fill() + f"{E}[7;7H{E}[?1049h{E}[2J{E}[HA{E}[?1049lB",
    "alt_47": fill() + f"{E}[?47h{E}[2J{E}[HALT47bare{E}[?47l",
    "alt_1048": fill() + f"{E}[9;9H{E}[?1048h{E}[1;1H{E}[?1048lQ",
    "alt_scroll_isolated": fill() + f"{E}[?1049h{E}[2J{E}[24;1Hbottom\n\nafter{E}[?1049l",
    # --- SGR ----------------------------------------------------------------
    "sgr_basic": f"{E}[2J{E}[H{E}[1mbold{E}[0m {E}[3mit{E}[0m {E}[4mul{E}[0m {E}[7mrev{E}[0m {E}[9mst{E}[0m {E}[5mbl{E}[0m",
    "sgr_colors": f"{E}[2J{E}[H{E}[31mr{E}[32mg{E}[44mb{E}[90mbr{E}[100mbrb{E}[0m",
    "sgr_256": f"{E}[2J{E}[H{E}[38;5;196mA{E}[48;5;21mB{E}[0m",
    "sgr_rgb": f"{E}[2J{E}[H{E}[38;2;10;20;30mA{E}[48;2;200;100;50mB{E}[0m",
    "sgr_rgb_colon": f"{E}[2J{E}[H{E}[38:2::10:20:30mA{E}[0m",
    "sgr_underline_styles": f"{E}[2J{E}[H{E}[4:3mcurly{E}[4:0mnone{E}[21mdouble{E}[0m",
    "sgr_reset_pairs": f"{E}[2J{E}[H{E}[1;3;4;7;9mall{E}[22;23;24;27;29mnone{E}[0m",
    "sgr_default_colors": f"{E}[2J{E}[H{E}[31;41mx{E}[39my{E}[49mz",
    "sgr_empty_params": f"{E}[2J{E}[H{E}[;;;mA{E}[mB",
    "sgr_faint": f"{E}[2J{E}[H{E}[2mfaint{E}[22mnormal",
    # --- modes / misc -------------------------------------------------------
    "irm_insert": f"{E}[2J{E}[Habcdefgh{E}[1;3H{E}[4hXY{E}[4l",
    "lnm": f"{E}[2J{E}[H{E}[20hline1\nline2{E}[20l",
    "decaln": f"{E}[2J{E}#8",
    "decstr": fill() + f"{E}[5;10r{E}[?6h{E}[1m{E}[!p{E}[1;1HA",
    "ris": fill() + f"{E}[?1049h{E}[1;31m{E}cAFTER RESET",
    "decdwl": f"{E}[2J{E}[Hnormal\r\n{E}#6double wide\r\n{E}#3top{E}[4;1H{E}#4bot",
    "mouse_modes": f"{E}[2J{E}[H{E}[?1000h{E}[?1002h{E}[?1003h{E}[?1006htext{E}[?1000l",
    "bracketed_paste": f"{E}[2J{E}[H{E}[?2004hpasted{E}[?2004l",
    "cursor_visibility": f"{E}[2J{E}[H{E}[?25lhidden{E}[?25hshown{E}[?25l",
    # --- absurd or missing parameters --------------------------------------
    "absurd_params": fill() + f"{E}[99999999999;1H{E}[99999999999A{E}[0;0;0;0;0;0;0;0;0;0mX",
    "absurd_scroll_region": fill() + f"{E}[20;5rX{E}[0;0rY{E}[1;1rZ",
    "unknown_csi": f"{E}[2J{E}[H{E}[99999Z{E}[<>?!q{E}[9999999999999999999@ok",
    "truncated_seqs": f"{E}[2J{E}[H{E}[{E}[1;{E}]0;title{E}\\visible",
    "c1_controls": f"{E}[2J{E}[Habc\x84def\x85ghi",
    "nul_and_del": f"{E}[2J{E}[Ha\x00b\x7fc",
    "bs_at_col0": f"{E}[2J{E}[H\b\b\bX",
    "cr_lf_ff_vt": f"{E}[2J{E}[Ha\x0bb\x0cc\rd",
    # --- unicode ------------------------------------------------------------
    "wide_runes": f"{E}[2J{E}[H你好世界 ok\r\n" + "中" * 41,
    "wide_overwrite": f"{E}[2J{E}[H你好{E}[1;2HX",
    "combining": f"{E}[2J{E}[Héà done",
    "emoji": f"{E}[2J{E}[H\U0001f600\U0001f680 ok",
    "powerline": f"{E}[2J{E}[H{E}[38;5;15;48;5;31m user {E}[38;5;31;48;5;240m{E}[38;5;250m ~/dev {E}[0m",
    "utf8_split_safe": f"{E}[2J{E}[H" + "é" * 40,
    "invalid_utf8_marker": f"{E}[2J{E}[Ha",
}

# A couple of raw-byte cases that cannot be expressed as str.
raw = {
    "invalid_utf8": b"\x1b[2J\x1b[Ha\xffb\xc3(c\xe2\x82d",
    "utf8_truncated_tail": b"\x1b[2J\x1b[Hok\xe4\xbd",
}

for name, s in cases.items():
    with open(os.path.join(out, name + ".vt"), "wb") as f:
        f.write(s.encode("utf-8"))
for name, b in raw.items():
    with open(os.path.join(out, name + ".vt"), "wb") as f:
        f.write(b)
print(len(cases) + len(raw), "cases")
