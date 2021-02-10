package util

type Greeter interface {
	Greet(person string, message string) error
}
