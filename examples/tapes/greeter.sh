# Asks for a name and answers. The program under test in quickstart.tape.
printf 'name? '
read -r name
printf 'hello, %s\n' "$name"
