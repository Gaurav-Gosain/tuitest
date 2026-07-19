# snap spawns a program on a real PTY, waits for it to stop drawing, and prints
# the screen. It asserts nothing and writes nothing, so it is how you find out
# what a TUI actually draws before writing a tape against it. Here it captures
# vim twice at different widths, which is how a layout claim gets checked
# without a terminal anywhere in the loop.
Output docs/images/snap.gif

Source scripts/demo/common.tape

Set Width 1000
Set Height 700

Hide
Type "clear" Enter
Show

Type "tuitest snap --size 84x14 -- vim scripts/demo/tapes/readme.tape" Sleep 400ms Enter
Sleep 4s

Type "tuitest snap --size 52x14 -- vim scripts/demo/tapes/readme.tape" Sleep 400ms Enter
Sleep 4.5s
