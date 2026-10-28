//go:build !wasip1

package logging

// Native builds (tests, fuzzing) have no host to log to.

func Info(string)  {}
func Warn(string)  {}
func Debug(string) {}
func Trace(string) {}
