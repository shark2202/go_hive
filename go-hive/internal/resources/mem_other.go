//go:build !darwin && !linux

package resources

// Other platforms have no probe in this port: the values stay null exactly like
// upstream's failed psutil.virtual_memory() branch (the verdict then skips the
// memory dimension and still classifies on the renderer count).

func systemMemorySample() systemMemory { return systemMemory{} }
