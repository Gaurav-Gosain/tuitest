# Reports its environment and its terminal size, then the size again after
# every line of input, until end of input.
echo "TERM=$TERM GREETING=$GREETING"
stty size
while read -r _; do
	stty size
done
