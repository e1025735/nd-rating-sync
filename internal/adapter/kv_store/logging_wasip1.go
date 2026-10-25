//go:build wasip1

package adapter

import pdk "github.com/extism/go-pdk"

func kvLogInfo(msg string)  { pdk.Log(pdk.LogInfo, msg) }
func kvLogWarn(msg string)  { pdk.Log(pdk.LogWarn, msg) }
func kvLogDebug(msg string) { pdk.Log(pdk.LogDebug, msg) }
func kvLogTrace(msg string) { pdk.Log(pdk.LogTrace, msg) }
