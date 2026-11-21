//go:build windows

package arghelp

// terminalColumns has no stdlib windows equivalent without new dependencies;
// the COLUMNS env var (honored first by argparse too) and the 80-column
// fallback cover the scripted and redirected cases.
func terminalColumns() int { return 0 }
