package main

import (
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	subsonicadapter "github.com/e1025735/nd-rating-sync/internal/adapter/subsonic"
	scanner "github.com/e1025735/nd-rating-sync/internal/scanner"
)

// Compatibility aliases kept for the root test helpers and legacy package-level
// references. The actual scheduling flow is now direct to the internal scanner.
type subsonicSong = subsonicadapter.SubsonicSong

type FileRecord = kvadapter.FileRecord

type fileReadResult = scanner.FileReadResult

const (
	tagFound       = scanner.TagFound
	tagAbsent      = scanner.TagAbsent
	fileUnreadable = scanner.FileUnreadable
)

func readAudioMetadata(path, ext string) ([]byte, bool) {
	return scanner.ReadAudioMetadata(path, ext)
}

func extractStarsFromFile(path string, suffix string, tagOrder []string) (int, fileReadResult) {
	stars, result := scanner.ExtractStarsFromFile(path, suffix, tagOrder)
	return stars, fileReadResult(result)
}

func dispatchParser(path string, ext string, data []byte, tagOrder []string) (stars int, ok, supported bool) {
	return scanner.DispatchParser(path, ext, data, tagOrder)
}

func ensureLibraryIndexed(libraryID string, deadline time.Time) (bool, error) {
	return scanner.EnsureLibraryIndexed(libraryID, deadline)
}

func toScannerConfig(cfg pluginConfig) scanner.PluginConfig {
	out := scanner.PluginConfig{
		SyncSchedule:                 cfg.SyncSchedule,
		MaxSongsPerRun:               cfg.MaxSongsPerRun,
		IncrementalSync:              cfg.IncrementalSync,
		DryRun:                       cfg.DryRun,
		CacheLibrariesFilesystemTree: cfg.CacheLibrariesFilesystemTree,
		DefaultSkipAlreadyRated:      cfg.DefaultSkipAlreadyRated,
		DefaultClearRatingIfUntagged: cfg.DefaultClearRatingIfUntagged,
		KVStorageMaxSize:             cfg.KVStorageMaxSize,
	}
	out.Libraries = make([]scanner.LibraryConfig, len(cfg.Libraries))
	for i, lib := range cfg.Libraries {
		out.Libraries[i] = toScannerLibrary(lib)
	}
	return out
}

func toScannerLibrary(lib libraryConfig) scanner.LibraryConfig {
	out := scanner.LibraryConfig{LibraryID: lib.LibraryID, LibraryName: lib.LibraryName}
	out.Users = make([]scanner.UserConfig, len(lib.Users))
	for i, u := range lib.Users {
		out.Users[i] = toScannerUser(u)
	}
	return out
}

func toScannerUser(u userConfig) scanner.UserConfig {
	return scanner.UserConfig{
		Username:              u.Username,
		SkipAlreadyRated:      u.SkipAlreadyRated,
		ClearRatingIfUntagged: u.ClearRatingIfUntagged,
		RatingTagOrder:        u.RatingTagOrder,
	}
}
