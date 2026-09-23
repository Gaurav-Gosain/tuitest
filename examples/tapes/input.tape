# What each input verb sends. showbytes.sh runs cat -v, which prints a line
# of input once Enter ends it, with ESC shown as ^[ and control bytes as ^X.
#
#   tuitest run input.tape

Set Size 60 10
Spawn sh showbytes.sh
Wait /ready/

# Key takes named keys, chords and single runes.
Key Up Ctrl+a Alt+x F5
Key Enter
Wait /\^\[\[A\^A\^\[x\^\[\[15~/

# Paste wraps the text in bracketed-paste markers.
Paste "pasted text"
Key Enter
Wait /\^\[\[200~pasted text\^\[\[201~/

# Raw writes a Go-quoted string byte for byte.
Raw "\x1b[1;2;3m"
Key Enter
Wait /\^\[\[1;2;3m/

# Mouse sends an SGR report: button 0 plus 16 for Ctrl, 1-based coordinates.
Mouse Press Left 10 5 +Ctrl
Key Enter
Wait /\^\[\[<16;11;6M/

# Focus sends the focus-in report of mode 1004.
Focus In
Key Enter
Wait /\^\[\[I$/ +Line

Snapshot input
