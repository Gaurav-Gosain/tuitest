# The README's first image: a real file manager being driven hard through a
# pseudo-terminal, with tuitest's own command trace scrolling underneath it.
# hero-session.sh builds the screen; this file only films it.
Output docs/images/hero.gif

Source scripts/demo/common.tape

Set Width 866
Set Height 598
Set FontSize 14
Set Framerate 12

Hide
Type "clear" Enter
Show

Type "scripts/demo/hero-session.sh" Enter
Sleep 20s
