# The tape behind the README's first image. It drives lazygit, a real program
# nobody wrote for this project, through bursts of keys, a mouse click, a
# scroll and a typed search, and asserts on what the program drew.
#
# Every WaitStable is a condition on the program going quiet, so each burst
# lands as fast as lazygit can answer it. The Sleeps are for the camera: they
# hold each result on screen long enough to be read at a normal frame rate, and
# they are the only fixed clock in the file.
Set Size 86 22

Spawn lazygit
Wait /Local branches/ @15s
Sleep 800ms

# Walk the commit history. Each burst of keys redraws the diff on the right.
Key 4
WaitStable @250ms
Sleep 600ms
Key Down Down Down
WaitStable @250ms
Sleep 700ms
Key Down Down Down Down
WaitStable @250ms
Sleep 800ms

# Open the commit and come back out.
Key Enter
WaitStable @400ms
Sleep 1100ms
Key Escape
WaitStable @400ms
Sleep 700ms

# A mouse click moves the focus, then the wheel scrolls under the pointer.
Mouse Press Left 12 15
Mouse Release Left 12 15
WaitStable @400ms
Sleep 800ms
Mouse Press WheelDown 12 15
Mouse Press WheelDown 12 15
Mouse Press WheelDown 12 15
WaitStable @400ms
Sleep 900ms

# Typed text goes through the same pseudo-terminal as everything else, so the
# search field fills in a character at a time, and then n jumps the selection
# to the next commit that matched.
Key 4
WaitStable @300ms
Sleep 400ms
Key /
WaitStable @300ms
Sleep 400ms
Type tape
WaitStable @500ms
Sleep 1200ms
Key Enter
WaitStable @500ms
Sleep 1000ms
Key n
WaitStable @400ms
Sleep 1000ms
Key Escape
WaitStable @400ms
Sleep 700ms

Expect /Commits/

Key q
ExpectExit 0
