//go:build !linux

package ratelimit

func Available() bool {
	return false
}

func Apply(tableName string, rules []Rule) error {
	return nil
}
