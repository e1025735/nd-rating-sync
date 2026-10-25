//go:build wasip1

package adapter

import pdk "github.com/extism/go-pdk"

func subsonicLogDebug(msg string) { pdk.Log(pdk.LogDebug, msg) }
func subsonicLogTrace(msg string) { pdk.Log(pdk.LogTrace, msg) }
