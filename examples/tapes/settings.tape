# Set, Resize, the quieter waits, Hide and Show, and Sleep.
#
#   tuitest run settings.tape

# Set configures the next Spawn, so it goes above it.
Set Size 40 10
Set Term vt100
Set Env GREETING=hi
Set WaitTimeout 5s
Set StabilizeInterval 100ms
Spawn sh sizes.sh

# WaitOutput passes once the program has written anything at all.
WaitOutput
Wait /TERM=vt100 GREETING=hi/
Wait /10 40/

# Resize sends SIGWINCH; the program sees the new size on its next stty.
Resize 60 12
Key Enter
Wait /12 60/

# WaitStable passes once output has been quiet for StabilizeInterval.
WaitStable

# Snapshot does nothing between Hide and Show, so no golden is read or written.
Hide
Snapshot never-written
Show

# Sleep exists, but run --strict rejects it: wait on a condition instead.
Sleep 50ms

# Ctrl+d on an empty line is end of input, so the loop ends and sh exits 0.
Key Ctrl+d
ExpectExit 0
