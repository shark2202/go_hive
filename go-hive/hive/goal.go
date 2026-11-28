package hive

import "github.com/shark2202/go_hive/go-hive/internal/schemas"

// Goal is a structured objective with weighted success criteria and
// constraints. Colonies execute against it; an independent loop can carry one
// as context.
//
// Parity: framework/schemas/goal.py::Goal
type Goal = schemas.Goal

// GoalStatus is a goal's lifecycle state.
//
// Parity: framework/schemas/goal.py::GoalStatus
type GoalStatus = schemas.GoalStatus

// Goal states.
const (
	GoalDraft     = schemas.GoalDraft
	GoalReady     = schemas.GoalReady
	GoalActive    = schemas.GoalActive
	GoalCompleted = schemas.GoalCompleted
	GoalFailed    = schemas.GoalFailed
)

// SuccessCriterion is one measurable condition of a goal.
//
// Parity: framework/schemas/goal.py::SuccessCriterion
type SuccessCriterion = schemas.SuccessCriterion

// Constraint is one restriction a goal's solution must respect.
//
// Parity: framework/schemas/goal.py::Constraint
type Constraint = schemas.Constraint

// OrderedMap is a JSON object that keeps insertion order.
//
// Parity-exempt: Go-only ordered representation of the Python goal context
// dict; Python dicts keep insertion order natively.
type OrderedMap = schemas.OrderedMap

// Timestamp is a schema timestamp serialized the way the runtime expects.
//
// Parity-exempt: Go-only schema timestamp value.
type Timestamp = schemas.Timestamp
