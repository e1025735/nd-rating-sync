//go:build !wasip1

package main

func logInfo(msg string)  {}
func logDebug(msg string) {}
func logWarn(msg string)  {}

// configValues is the test-injectable config store. Tests swap the whole map
// and restore it via t.Cleanup so tests stay isolated.
var configValues = map[string]string{}

func getConfig(key string) (string, bool) {
	v, ok := configValues[key]
	return v, ok
}
