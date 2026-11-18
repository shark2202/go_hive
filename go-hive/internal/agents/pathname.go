package agents

// Parity: framework/agents/discovery.py::_find_worker_configs

import "strings"

func pyStemSuffix(name string) (string, string) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i >= len(name)-1 {
		return name, ""
	}
	return name[:i], name[i:]
}
