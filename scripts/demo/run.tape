# Hero recording: a tape tests a third-party program with no Go anywhere, and
# a stale assertion fails with a diff and the screen it was judged against.
Output docs/images/run.gif

Require tuitest

Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font Mono"
Set FontSize 15
Set Width 1000
Set Height 620
Set Padding 22
Set Framerate 12
Set PlaybackSpeed 1.0
Set BorderRadius 8
Set Theme { "name": "tuitest", "background": "#0b0e14", "foreground": "#e6e9ef", "cursor": "#7fd88f", "selection": "#1f2a38", "black": "#0b0e14", "red": "#e88388", "green": "#7fd88f", "yellow": "#dbc074", "blue": "#7fb4d8", "magenta": "#c99ee0", "cyan": "#5ad1c8", "white": "#e6e9ef", "brightBlack": "#59606f", "brightRed": "#e88388", "brightGreen": "#7fd88f", "brightYellow": "#dbc074", "brightBlue": "#7fb4d8", "brightMagenta": "#c99ee0", "brightCyan": "#5ad1c8", "brightWhite": "#ffffff" }

Hide
Type "clear" Enter
Show

Type "cat scripts/demo/tapes/readme.tape" Sleep 400ms Enter
Sleep 2.5s

Type "tuitest run scripts/demo/tapes/readme.tape; echo exit=$?" Sleep 400ms Enter
Sleep 3s

Type "clear" Enter
Sleep 300ms

Type "tuitest run scripts/demo/tapes/readme-stale.tape; echo exit=$?" Sleep 400ms Enter
Sleep 6s
