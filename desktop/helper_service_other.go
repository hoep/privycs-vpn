//go:build !windows

package main

// runHelperEntrypoint is the cross-platform entry for `--helper`. On non-Windows
// there's no Service Control Manager to satisfy, so we just run the helper
// listener directly. On Windows, the build-tagged counterpart handles SCM.
func runHelperEntrypoint() {
	RunHelperMode()
}
