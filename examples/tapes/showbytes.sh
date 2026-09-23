# Prints every line of input with control bytes made visible, so a tape can
# show exactly what each input verb sends. The PTY does not echo, so only
# cat's output reaches the screen.
echo ready
exec cat -v
