# Test a third-party program with no Go anywhere: page the repository README
# in less, check the heading and the summary line, quit cleanly.
Set Size 76 16
Spawn less README.md
Wait /# tuitest/
Expect /headless testing harness for terminal programs/
Key q
ExpectExit 0
