// Package logging sends the plugin's log lines to the host, skipping lines
// below the host's configured level before any host memory is allocated.
//
// pdk.Log allocates the message in host memory before it checks the level,
// and never frees it (the PDK leaves the free to the SDKs), so every filtered
// trace or debug line would still cost an allocation for the rest of the
// callback. Only the wasip1 build talks to the host; in native builds (tests)
// the functions are no-ops.
package logging
