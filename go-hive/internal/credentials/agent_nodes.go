package credentials

// Parity: framework/credentials/setup.py::load_agent_nodes
//
// The credential-relevant projection of an agent directory: the tools and node
// types the agent declares across its nodes, which is what
// validate_agent_credentials keys off. Upstream reads a declarative
// ``agent.json`` (framework.loader.agent_loader.load_agent_config) and falls
// back to importing ``agent.py`` for legacy Python agents.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// AgentNode is the credential-relevant projection of one graph node. It is the
// Go counterpart of the NodeSpec attributes validate_agent_credentials reads
// (node.tools / node.node_type).
type AgentNode struct {
	Tools    []string
	NodeType string
}

// LoadAgentNodes loads the nodes of an agent directory the way upstream
// load_agent_nodes does:
//
//  1. “<dir>/agent.json“ — parsed with the same strictness as the Pydantic
//     AgentConfig the reference uses; any validation failure yields no nodes
//     (upstream swallows the error and returns []).
//  2. “<dir>/agent.py“ — upstream imports the module and returns its
//     “nodes“. The Go runtime has no Python interpreter, so this branch
//     reports no nodes (documented divergence; Python agents are not runnable
//     here in the first place).
//  3. Neither file — no nodes.
func LoadAgentNodes(agentPath string) []AgentNode {
	if fileExists(filepath.Join(agentPath, "agent.json")) {
		return loadDeclarativeAgentNodes(filepath.Join(agentPath, "agent.json"))
	}
	if fileExists(filepath.Join(agentPath, "agent.py")) {
		return []AgentNode{}
	}
	return []AgentNode{}
}

// loadDeclarativeAgentNodes mirrors load_agent_config's node/tool resolution.
//
// Required-field strictness follows the AgentConfig schema: name, goal (with
// description), nodes, edges and entry_node must be present and typed; every
// node must carry a string id. A document failing any of these reads as no
// nodes, exactly like the reference's except-Exception catch. Edge element
// validation is limited to from_node/to_node presence — edge payloads do not
// affect which credentials a node needs.
func loadDeclarativeAgentNodes(path string) []AgentNode {
	data, err := os.ReadFile(path)
	if err != nil {
		return []AgentNode{}
	}
	nodes, ok := parseDeclarativeNodes(data)
	if !ok {
		return []AgentNode{}
	}
	return nodes
}

func parseDeclarativeNodes(data []byte) ([]AgentNode, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var doc map[string]json.RawMessage
	if err := decoder.Decode(&doc); err != nil {
		return nil, false
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, false
	}
	if doc == nil {
		return nil, false
	}

	// Required top-level fields (Pydantic: no default).
	nameRaw, present := doc["name"]
	if !present {
		return nil, false
	}
	var name string
	if err := json.Unmarshal(nameRaw, &name); err != nil {
		return nil, false
	}
	var entryNode string
	if err := json.Unmarshal(doc["entry_node"], &entryNode); err != nil {
		return nil, false
	}
	goalRaw, ok := doc["goal"]
	if !ok {
		return nil, false
	}
	var goal map[string]json.RawMessage
	if err := json.Unmarshal(goalRaw, &goal); err != nil || goal == nil {
		return nil, false
	}
	var goalDescription string
	if err := json.Unmarshal(goal["description"], &goalDescription); err != nil {
		return nil, false
	}

	agentTools, ok := parseToolAccess(doc["tools"])
	if !ok {
		return nil, false
	}

	edgesRaw, ok := doc["edges"]
	if !ok {
		return nil, false
	}
	var edges []map[string]json.RawMessage
	if err := json.Unmarshal(edgesRaw, &edges); err != nil {
		return nil, false
	}
	for _, edge := range edges {
		var fromNode, toNode string
		if err := json.Unmarshal(edge["from_node"], &fromNode); err != nil {
			return nil, false
		}
		if err := json.Unmarshal(edge["to_node"], &toNode); err != nil {
			return nil, false
		}
	}

	nodesRaw, ok := doc["nodes"]
	if !ok {
		return nil, false
	}
	var nodeDocs []map[string]json.RawMessage
	if err := json.Unmarshal(nodesRaw, &nodeDocs); err != nil {
		return nil, false
	}

	nodes := make([]AgentNode, 0, len(nodeDocs))
	for _, nodeDoc := range nodeDocs {
		if nodeDoc == nil {
			return nil, false
		}
		var nodeID string
		if err := json.Unmarshal(nodeDoc["id"], &nodeID); err != nil {
			return nil, false
		}
		nodeType := "event_loop"
		if raw, present := nodeDoc["node_type"]; present {
			if err := json.Unmarshal(raw, &nodeType); err != nil {
				return nil, false
			}
		}
		nodeTools, ok := parseToolAccess(nodeDoc["tools"])
		if !ok {
			return nil, false
		}

		// Tool resolution: node-level explicit -> node-level none -> inherit.
		// Parity: agent_loader.py::load_agent_config
		var toolsList []string
		switch {
		case nodeTools.Policy == "explicit" && len(nodeTools.Allowed) > 0:
			toolsList = nodeTools.Allowed
		case nodeTools.Policy == "none":
			toolsList = []string{}
		case agentTools.Policy == "explicit" && len(agentTools.Allowed) > 0:
			toolsList = agentTools.Allowed
		default:
			toolsList = []string{}
		}
		tools := make([]string, len(toolsList))
		copy(tools, toolsList)
		nodes = append(nodes, AgentNode{Tools: tools, NodeType: nodeType})
	}
	return nodes, true
}

type toolAccess struct {
	Policy  string
	Allowed []string
}

// parseToolAccess reads a ToolAccessConfig-shaped object; absent reads as the
// schema default (policy explicit, allowed empty). A non-object value is
// invalid for the schema, so it reports ok=false.
func parseToolAccess(raw json.RawMessage) (toolAccess, bool) {
	access := toolAccess{Policy: "explicit"}
	if len(raw) == 0 {
		return access, true
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return toolAccess{}, false
	}
	if rawPolicy, present := doc["policy"]; present {
		if err := json.Unmarshal(rawPolicy, &access.Policy); err != nil {
			return toolAccess{}, false
		}
		if access.Policy == "all" {
			return toolAccess{}, false
		}
	}
	allowedRaw, present := doc["allowed"]
	if !present {
		allowedRaw = doc["tools"] // Pydantic alias
	}
	if len(allowedRaw) > 0 {
		var allowed []string
		if err := json.Unmarshal(allowedRaw, &allowed); err != nil {
			return toolAccess{}, false
		}
		access.Allowed = allowed
	}
	if access.Allowed == nil {
		access.Allowed = []string{}
	}
	return access, true
}

func ensureEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing data")
		}
		return err
	}
	return nil
}
