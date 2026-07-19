# record drives a program by hand and writes down what you did as a tape, so
# the first version of a test is produced by using the program rather than by
# writing assertions. Ctrl+] stops the recording. The tape it emits is then
# shown and run, which is the honest check that the recording replays.
#
# The program under test is testdata/echotui, a line-echoing fixture: it is
# small enough that every keystroke below is visible in the tape that comes out.
Output docs/images/record-replay.gif

Source scripts/demo/common.tape

Set Width 1000
Set Height 700

Hide
Type "clear" Enter
Show

Type "tuitest record --cols 60 --rows 8 --quiet 500ms -o /tmp/echo.tape -- echotui" Sleep 400ms Enter
Sleep 3.5s

Type "hello" Sleep 900ms Enter
Sleep 1.2s
Type "tuitest" Sleep 900ms Enter
Sleep 1.2s
Type "quit" Sleep 900ms Enter
Sleep 1.5s

Ctrl+]
Sleep 1.5s

Type "clear" Enter
Sleep 300ms

Type "cat /tmp/echo.tape" Sleep 400ms Enter
Sleep 4s

Type "tuitest run /tmp/echo.tape; echo exit=$?" Sleep 400ms Enter
Sleep 4s
