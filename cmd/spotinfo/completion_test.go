package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteCompletion_Bash(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := writeCompletion(&buf, "bash")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "complete")
	assert.Contains(t, buf.String(), "spotinfo")
}

func TestWriteCompletion_Zsh(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := writeCompletion(&buf, "zsh")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "compdef")
	assert.Contains(t, buf.String(), "spotinfo")
}

func TestWriteCompletion_Fish(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := writeCompletion(&buf, "fish")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "complete -c spotinfo")
}

func TestWriteCompletion_UnsupportedShell(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := writeCompletion(&buf, "powershell")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported shell")
}

func TestCompletionCommand_HasSubcommands(t *testing.T) {
	t.Parallel()
	cmd := completionCommand()
	assert.Equal(t, "completion", cmd.Name)
	assert.Len(t, cmd.Subcommands, 3)

	names := make([]string, len(cmd.Subcommands))
	for i, sub := range cmd.Subcommands {
		names[i] = sub.Name
	}
	assert.Contains(t, names, "bash")
	assert.Contains(t, names, "zsh")
	assert.Contains(t, names, "fish")
}
