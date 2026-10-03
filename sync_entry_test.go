//go:build !tinygo

package main

import (
	"testing"

	scanner "github.com/e1025735/nd-rating-sync/internal/scanner"
	"github.com/stretchr/testify/require"
)

func TestRunSyncStep_NoLibraries(t *testing.T) {
	err := scanner.RunSyncStep(scanner.PluginConfig{}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no libraries configured")
}
