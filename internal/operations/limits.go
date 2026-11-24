package operations

import (
	"hclaro/internal/engine"
	"hclaro/internal/graph"
)

// Limits are the numbers the UI quotes in its text. /session serves them so the UI keeps no
// copy; each field reads the constant that enforces it.
type Limits struct {
	MaxReplace        int `json:"maxReplace"`
	Operations        int `json:"operations"`
	OperationLogs     int `json:"operationLogs"`
	DriftScans        int `json:"driftScans"`
	DriftMaxAgeHours  int `json:"driftMaxAgeHours"`
	Graphs            int `json:"graphs"`
	GraphMaxAgeHours  int `json:"graphMaxAgeHours"`
	GraphMaxMebibytes int `json:"graphMaxMebibytes"`
	GraphMaxNodes     int `json:"graphMaxNodes"`
	GraphMaxEdges     int `json:"graphMaxEdges"`
}

func ServedLimits() Limits {
	return Limits{
		MaxReplace: engine.MaxReplace, Operations: maxOperations, OperationLogs: maxLogs,
		DriftScans: maxScans, DriftMaxAgeHours: int(DriftMaxAge.Hours()),
		Graphs: maxGraphs, GraphMaxAgeHours: int(GraphMaxAge.Hours()),
		GraphMaxMebibytes: graph.MaxInputBytes >> 20, GraphMaxNodes: graph.MaxDOTNodes, GraphMaxEdges: graph.MaxDOTEdges,
	}
}
