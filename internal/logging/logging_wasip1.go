//go:build wasip1

package logging

import pdk "github.com/extism/go-pdk"

// hostLogLevel is the level the host logs at; the same host function the
// PDK's LogMemory consults.
//
//go:wasmimport extism:host/env get_log_level
func hostLogLevel() int32

// level is read on first use. Every callback runs in a fresh module
// instance, so it is read once per callback.
var level int32 = -1

func enabled(l pdk.LogLevel) bool {
	if level < 0 {
		level = hostLogLevel()
	}
	return int32(l) >= level
}

// log sends msg to the host at level l, unless the host filters that level.
func log(l pdk.LogLevel, msg string) {
	if enabled(l) {
		pdk.Log(l, msg)
	}
}

func Info(msg string)  { log(pdk.LogInfo, msg) }
func Warn(msg string)  { log(pdk.LogWarn, msg) }
func Debug(msg string) { log(pdk.LogDebug, msg) }
func Trace(msg string) { log(pdk.LogTrace, msg) }
