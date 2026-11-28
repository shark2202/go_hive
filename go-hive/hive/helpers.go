package hive

import "sort"

func sortStrings(values []string) {
	sort.Strings(values)
}

func ptr[T any](value T) *T {
	return &value
}
