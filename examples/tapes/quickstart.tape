# The smallest useful tape: start a program, answer its prompt, check the
# reply, pin the screen, and check that it exited cleanly.
#
# Run it from this directory, since Spawn resolves paths from where you are:
#
#   tuitest run quickstart.tape
#
# The first run fails because testdata/greeting.golden is missing; record it
# with --update, then review it like any other file in the diff.

Set Size 40 5
Spawn sh greeter.sh
Wait /name\?/
Type gopher
Key Enter
Wait /hello, gopher/
Expect /hello, \w+/
Snapshot greeting
ExpectExit 0
