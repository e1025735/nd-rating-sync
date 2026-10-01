package media

// maxMetadataReadBytes is the per-format upper bound on bytes the metadata
// extractors will pull into memory from any single file.
const maxMetadataReadBytes = 16 * 1024 * 1024
