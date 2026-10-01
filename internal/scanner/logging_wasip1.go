//go:build wasip1

package scanner

import pdk "github.com/extism/go-pdk"

func logInfo(msg string)  { pdk.Log(pdk.LogInfo, msg) }
func logDebug(msg string) { pdk.Log(pdk.LogDebug, msg) }
func logTrace(msg string) { pdk.Log(pdk.LogTrace, msg) }
func logWarn(msg string)  { pdk.Log(pdk.LogWarn, msg) }
