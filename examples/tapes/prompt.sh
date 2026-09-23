# A stand-in for a shell with OSC 133 shell integration: it marks the prompt
# (A, B), the command output (C) and the command's exit status (D).
#
# The sleeps are there because WaitPrompt and WaitCommand wait for a marker
# newer than the wait itself, so a marker that arrives before the tape reaches
# the wait is not seen. A real shell is slow enough that this rarely matters.
sleep 0.2
printf '\033]133;A\007$ \033]133;B\007'
while read -r _; do
	sleep 0.2
	printf '\033]133;C\007ran\n\033]133;D;0\007'
	printf '\033]133;A\007$ \033]133;B\007'
done
