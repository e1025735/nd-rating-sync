//go:build wasip1

package main

import pdk "github.com/extism/go-pdk"

func logInfo(msg string)  { pdk.Log(pdk.LogInfo, msg) }
func logDebug(msg string) { pdk.Log(pdk.LogDebug, msg) }
func logWarn(msg string)  { pdk.Log(pdk.LogWarn, msg) }

func getConfig(key string) (string, bool) { return pdk.GetConfig(key) }
