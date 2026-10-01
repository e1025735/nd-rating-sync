package main

import (
	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	scanner "github.com/e1025735/nd-rating-sync/internal/scanner"
)

type FileRecord = kvadapter.FileRecord

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
