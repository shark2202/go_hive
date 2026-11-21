package pipeline

import "fmt"

// InputValidationStage rejects requests whose input_data does not match the
// entry point's declared input schema.
//
// Config (`config.schemas`) maps entry point id to `{required_key:
// expected_type}` with the Python type names configuration can carry: str,
// int, float, bool, list, dict (none/NoneType accepted too). Upstream passes
// Python type objects; the Go port names them, and every type-semantics quirk
// of isinstance is preserved (see pythonIsInstance).
//
// Parity: framework/pipeline/stages/input_validation.py::InputValidationStage
type InputValidationStage struct {
	BaseStage
	// Schemas maps entry point id to the ordered field expectations.
	Schemas map[string][]schemaField
}

// NewInputValidationStage builds the stage from its declarative config.
func NewInputValidationStage(spec map[string]any) (Stage, error) {
	config := configMap(spec)
	stage := &InputValidationStage{
		BaseStage: BaseStage{StageName: "input_validation", StageOrder: 100},
		Schemas:   map[string][]schemaField{},
	}
	rawSchemas, ok := config["schemas"].(map[string]any)
	if !ok {
		return stage, nil
	}
	for entryPoint, rawSchema := range rawSchemas {
		schema, ok := rawSchema.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("input_validation schemas[%q] must be an object", entryPoint)
		}
		stage.Schemas[entryPoint] = orderedSchemaFields(schema)
	}
	return stage, nil
}

// Process validates the request against its entry point's schema; an unknown
// entry point passes through, and the first missing/wrongly-typed key rejects.
//
// Parity: framework/pipeline/stages/input_validation.py::InputValidationStage.process
func (s *InputValidationStage) Process(ctx *Context) (Result, error) {
	schema := s.Schemas[ctx.EntryPointID]
	if len(schema) == 0 {
		return ContinueResult(), nil
	}
	for _, field := range schema {
		value, present := ctx.InputData[field.Key]
		if !present {
			return RejectResult(fmt.Sprintf("Missing required input key: '%s'", field.Key)), nil
		}
		matches, err := pythonIsInstance(value, field.Type)
		if err != nil {
			return Result{}, err
		}
		if !matches {
			return RejectResult(fmt.Sprintf(
				"Input key '%s' has type %s, expected %s",
				field.Key, pythonTypeName(value), field.Type,
			)), nil
		}
	}
	return ContinueResult(), nil
}
