//go:build wasip1

package media

import pdk "github.com/extism/go-pdk"

func logInfo(msg string)  { pdk.Log(pdk.LogInfo, msg) }
func logWarn(msg string)  { pdk.Log(pdk.LogWarn, msg) }
func logDebug(msg string) { pdk.Log(pdk.LogDebug, msg) }
func logTrace(msg string) { pdk.Log(pdk.LogTrace, msg) }
