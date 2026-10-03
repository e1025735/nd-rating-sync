//go:build !tinygo

package main

import (
	"encoding/json"
	"fmt"
	"testing"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/mock"
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

func mustJSONBytes(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal failed: %v", err))
	}
	return b
}

func resetSchedulerMock(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		host.SchedulerMock.ExpectedCalls = nil
		host.SchedulerMock.Calls = nil
	})
}

func init() {
	_ = kvadapter.KVKeyConfigHash
	_ = mock.Anything
}
