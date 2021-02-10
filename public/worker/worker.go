package worker

import (
	"errors"
	"strings"
)

type Worker interface {
	Do(name string) (*string, error)
}

func Do(name string) (*string, error) {
	if name == "" {
		return nil, errors.New("name cannot be empty")
	}
	text := strings.Repeat(name, 3)
	return &text, nil
}
