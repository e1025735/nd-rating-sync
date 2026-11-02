//go:build !tinygo

package main

import (
	"testing"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
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

// noSweepRecord makes the KV store hold no sweep record, which is what a run
// without libraries looks up before it marks a sweep interrupted.
func noSweepRecord(t *testing.T) {
	t.Helper()
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", kvadapter.SweepActiveKey).Return([]byte(nil), false, nil)
}

func resetSchedulerMock(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		host.SchedulerMock.ExpectedCalls = nil
		host.SchedulerMock.Calls = nil
	})
}
