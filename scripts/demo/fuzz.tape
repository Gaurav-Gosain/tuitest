# fuzz drives a program with randomised keys, mouse events and resizes, then
# minimises whatever it breaks into a tape that replays it. The program here is
# testdata/buggytui, a fixture that panics on F5 on purpose: it exists so the
# detectors can be shown finding a real fault rather than a hypothetical one.
# The last command runs the minimised tape back, which is the point of the
# whole exercise: a finding arrives as a regression test, not a seed number.
Output docs/images/fuzz.gif

Source scripts/demo/common.tape

Set Width 1100
Set Height 450

Hide
Type "clear" Enter
Show

Type "tuitest fuzz --duration 5s --actions 20 --seed 7 --corpus .demo-corpus -- buggytui -bug panic-on-key" Sleep 400ms Enter
Sleep 9s

Type "clear" Enter
Sleep 300ms

Type "head -9 .demo-corpus/crash-*.tape" Sleep 400ms Enter
Sleep 4s

Type "tuitest run .demo-corpus/crash-*.tape; echo exit=$?" Sleep 400ms Enter
Sleep 4s
