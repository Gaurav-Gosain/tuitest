package cli

import (
	"flag"
	"fmt"
	"sort"
	"strings"
)

func completionCommand() *Command {
	c := &Command{
		Name:    "completion",
		Summary: "print a shell completion script",
		Usage:   "completion bash|zsh|fish",
		Long: `Completion prints a script that completes tuitest's commands, their flags, and
tape file names for the given shell. Source it from your shell's startup file.

examples:
  tuitest completion bash > /etc/bash_completion.d/tuitest
  tuitest completion zsh  > "${fpath[1]}/_tuitest"
  tuitest completion fish > ~/.config/fish/completions/tuitest.fish`,
	}

	c.Run = func(env *Env, args []string) int {
		if len(args) != 1 {
			env.errorf("completion needs exactly one shell name")
			printCommandHelp(env.Stderr, c)
			return ExitUsage
		}
		var script string
		switch args[0] {
		case "bash":
			script = bashCompletion()
		case "zsh":
			script = zshCompletion()
		case "fish":
			script = fishCompletion()
		default:
			env.errorf("unsupported shell %q; want bash, zsh, or fish", args[0])
			return ExitUsage
		}
		fmt.Fprint(env.Stdout, script)
		return ExitOK
	}
	return c
}

// commandNames returns every command name plus help, for completion.
func commandNames() []string {
	names := []string{"help"}
	for _, c := range Commands() {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return names
}

// flagNames returns a command's flags as "-name", sorted.
func flagNames(c *Command) []string {
	if c.flags == nil {
		return nil
	}
	var out []string
	c.flags().VisitAll(func(f *flag.Flag) { out = append(out, "-"+f.Name) })
	sort.Strings(out)
	return out
}

func bashCompletion() string {
	var b strings.Builder
	b.WriteString(`# bash completion for tuitest. Source this file, or install it into
# /etc/bash_completion.d/.
_tuitest() {
  local cur cmd
  cur="${COMP_WORDS[COMP_CWORD]}"
  cmd="${COMP_WORDS[1]}"

  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W "` + strings.Join(commandNames(), " ") + `" -- "$cur") )
    return
  fi

  case "$cmd" in
`)
	for _, c := range Commands() {
		flags := flagNames(c)
		fmt.Fprintf(&b, "    %s)\n", c.Name)
		if len(flags) > 0 {
			fmt.Fprintf(&b, "      if [[ \"$cur\" == -* ]]; then\n")
			fmt.Fprintf(&b, "        COMPREPLY=( $(compgen -W \"%s\" -- \"$cur\") )\n", strings.Join(flags, " "))
			fmt.Fprintf(&b, "        return\n      fi\n")
		}
		switch c.Name {
		case "run":
			b.WriteString("      COMPREPLY=( $(compgen -f -X '!*.tape' -- \"$cur\") $(compgen -d -- \"$cur\") )\n")
		case "completion":
			b.WriteString("      COMPREPLY=( $(compgen -W \"bash zsh fish\" -- \"$cur\") )\n")
		case "snap":
			b.WriteString("      COMPREPLY=( $(compgen -c -- \"$cur\") )\n")
		}
		b.WriteString("      return\n      ;;\n")
	}
	b.WriteString(`    help)
      COMPREPLY=( $(compgen -W "` + strings.Join(commandNames(), " ") + `" -- "$cur") )
      return
      ;;
  esac
}
complete -F _tuitest tuitest
`)
	return b.String()
}

func zshCompletion() string {
	var b strings.Builder
	b.WriteString("#compdef tuitest\n# zsh completion for tuitest. Install as _tuitest on your fpath.\n\n_tuitest() {\n  local -a commands\n  commands=(\n")
	for _, c := range Commands() {
		fmt.Fprintf(&b, "    '%s:%s'\n", c.Name, c.Summary)
	}
	b.WriteString("    'help:show help for a command'\n  )\n\n")
	b.WriteString(`  if (( CURRENT == 2 )); then
    _describe 'command' commands
    return
  fi

  case "${words[2]}" in
`)
	for _, c := range Commands() {
		flags := flagNames(c)
		fmt.Fprintf(&b, "    %s)\n", c.Name)
		if len(flags) > 0 {
			fmt.Fprintf(&b, "      _arguments '*:flag:(%s)'\n", strings.Join(flags, " "))
		}
		switch c.Name {
		case "run":
			b.WriteString("      _files -g '*.tape'\n")
		case "completion":
			b.WriteString("      _values shell bash zsh fish\n")
		case "snap":
			b.WriteString("      _command_names -e\n")
		}
		b.WriteString("      ;;\n")
	}
	b.WriteString("    help)\n      _describe 'command' commands\n      ;;\n  esac\n}\n\n_tuitest \"$@\"\n")
	return b.String()
}

func fishCompletion() string {
	var b strings.Builder
	b.WriteString("# fish completion for tuitest. Install as ~/.config/fish/completions/tuitest.fish\n\n")
	b.WriteString("function __tuitest_no_command\n  set -l cmd (commandline -opc)\n  test (count $cmd) -eq 1\nend\n\n")
	for _, c := range Commands() {
		fmt.Fprintf(&b, "complete -c tuitest -n __tuitest_no_command -a %s -d '%s'\n", c.Name, c.Summary)
	}
	b.WriteString("complete -c tuitest -n __tuitest_no_command -a help -d 'show help for a command'\n\n")
	for _, c := range Commands() {
		if c.flags == nil {
			continue
		}
		c.flags().VisitAll(func(f *flag.Flag) {
			usage := strings.ReplaceAll(f.Usage, "'", "")
			fmt.Fprintf(&b, "complete -c tuitest -n '__fish_seen_subcommand_from %s' -o %s -d '%s'\n",
				c.Name, f.Name, usage)
		})
	}
	b.WriteString("\ncomplete -c tuitest -n '__fish_seen_subcommand_from run' -a '(__fish_complete_suffix .tape)'\n")
	b.WriteString("complete -c tuitest -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'\n")
	return b.String()
}
