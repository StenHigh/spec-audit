package fixture

import "strings"

func Flip(value bool) bool {
	return !value
}

func Double(value int) int {
	return value * 2
}

func Absolute(value int) int {
	return value
}

type Store interface {
	Put(string) error
}

func Save(store Store, value string) error {
	return store.Put(value)
}

func Normalize(value string) string {
	return strings.TrimSpace(value)
}
