# The same tape, still asserting wording the README has since changed. This is
# what a stale assertion looks like once the program under test has moved on:
# tuitest finds the closest line, points at the column that diverges, and
# prints the screen the assertion was judged against.
Set Size 76 16
Spawn less README.md
Wait /# tuitest/
Expect /A headless testing harness for terminal apps in Go/
Key q
ExpectExit 0
