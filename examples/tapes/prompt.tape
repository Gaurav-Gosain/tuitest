# WaitPrompt and WaitCommand synchronise on a shell's OSC 133 markers instead
# of on prompt text, which users configure and command output can contain.
#
#   tuitest run prompt.tape

Set Size 40 5
Spawn sh prompt.sh
WaitPrompt
Key Enter
WaitCommand
Expect /ran/
