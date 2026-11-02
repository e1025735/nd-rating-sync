//go:build wasip1

package main

import pdk "github.com/extism/go-pdk"

func getConfig(key string) (string, bool) { return pdk.GetConfig(key) }
