//go:build !wasip1

package main

func logInfo(msg string)  {}
func logDebug(msg string) {}
func logWarn(msg string)  {}
func logTrace(msg string) {}

// getConfig returns ("", false) for every key in non-WASM builds. Tests do
// not interact with this function — they call loadConfigFrom with their own
// closure over a local map, avoiding any global state.
func getConfig(key string) (string, bool) { return "", false }
