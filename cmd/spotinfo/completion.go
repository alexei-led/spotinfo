package main

import (
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v2"
)

func completionCommand() *cli.Command {
	return &cli.Command{
		Name:  "completion",
		Usage: "generate shell completion script",
		Subcommands: []*cli.Command{
			{
				Name:  "bash",
				Usage: "generate bash completion script",
				Action: func(_ *cli.Context) error {
					return writeCompletion(os.Stdout, "bash")
				},
			},
			{
				Name:  "zsh",
				Usage: "generate zsh completion script",
				Action: func(_ *cli.Context) error {
					return writeCompletion(os.Stdout, "zsh")
				},
			},
			{
				Name:  "fish",
				Usage: "generate fish completion script",
				Action: func(_ *cli.Context) error {
					return writeCompletion(os.Stdout, "fish")
				},
			},
		},
	}
}

func writeCompletion(w io.Writer, shell string) error {
	script, ok := completionScripts[shell]
	if !ok {
		return fmt.Errorf("unsupported shell: %s", shell)
	}
	_, err := fmt.Fprint(w, script)
	return err
}

var completionScripts = map[string]string{
	"bash": `#! /bin/bash

_spotinfo_completions() {
    local cur prev opts
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    # Get completions from urfave/cli
    if [[ "${cur}" == -* ]]; then
        COMPREPLY=( $(compgen -W "$(spotinfo --generate-bash-completion)" -- "${cur}") )
    else
        COMPREPLY=( $(compgen -W "$(spotinfo --generate-bash-completion)" -- "${cur}") )
    fi
    return 0
}

complete -o default -F _spotinfo_completions spotinfo
`,
	"zsh": `#compdef spotinfo

_spotinfo() {
    local -a opts
    local cur="${words[$CURRENT]}"

    # Get completions from urfave/cli
    opts=("${(@f)$(${words[1]} --generate-bash-completion 2>/dev/null)}")
    _describe 'spotinfo' opts
}

compdef _spotinfo spotinfo
`,
	"fish": `# Fish completion for spotinfo

complete -c spotinfo -f
complete -c spotinfo -l help -s h -d 'show help'
complete -c spotinfo -l version -s v -d 'print the version'
complete -c spotinfo -l type -d 'EC2 instance type (RE2 regexp pattern)' -r
complete -c spotinfo -l os -d 'instance OS (windows/linux)' -r -a 'linux windows'
complete -c spotinfo -l region -d 'AWS region' -r
complete -c spotinfo -l output -d 'output format' -r -a 'number text json table csv'
complete -c spotinfo -l cpu -d 'filter: minimal vCPU cores' -r
complete -c spotinfo -l memory -d 'filter: minimal memory GiB' -r
complete -c spotinfo -l price -d 'filter: maximum price per hour' -r
complete -c spotinfo -l sort -d 'sort results' -r -a 'interruption type savings price region score'
complete -c spotinfo -l order -d 'sort order' -r -a 'asc desc'
complete -c spotinfo -l profile -d 'config profile name' -r
complete -c spotinfo -l with-score -d 'include spot placement scores'
complete -c spotinfo -l min-score -d 'filter: minimum placement score' -r
complete -c spotinfo -l az -d 'request AZ-level scores'
complete -c spotinfo -l score-timeout -d 'score enrichment timeout in seconds' -r
complete -c spotinfo -l debug -d 'enable debug logging'
complete -c spotinfo -l quiet -d 'quiet mode'
complete -c spotinfo -n '__fish_use_subcommand' -a completion -d 'generate shell completion script'
complete -c spotinfo -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish' -d 'shell type'
`,
}
