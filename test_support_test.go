//go:build !tinygo

package main

import (
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

func resetKVStoreMock(t *testing.T) {
	t.Helper()
	host.KVStoreMock.ExpectedCalls = nil
	host.KVStoreMock.Calls = nil
	t.Cleanup(func() {
		host.KVStoreMock.ExpectedCalls = nil
		host.KVStoreMock.Calls = nil
	})
}

func resetSchedulerMock(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		host.SchedulerMock.ExpectedCalls = nil
		host.SchedulerMock.Calls = nil
	})
}
