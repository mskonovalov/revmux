package finding

import (
	_ "embed"
	"encoding/json"
	"slices"
)

// finderSchema is a copy of app/executor/testdata/finder-schema.json, the file the recorded
// CLI capture was taken under. The fixture is authoritative: schema_test.go asserts the two
// are identical, and a drift means every executor test asserts a shape the CLI never emits.
//
//go:embed finder-schema.json
var finderSchema []byte

//go:embed synthesis-schema.json
var synthesisSchema []byte

//go:embed verify-schema.json
var verifySchema []byte

// FinderSchema returns the contract a lens agent's findings must match. It is deliberately a
// subset of Finding: sources and verdict are omitted because Go assigns both, and a field the
// model can fill is a field it will fill.
func FinderSchema() json.RawMessage { return slices.Clone(finderSchema) }

// SynthesisSchema returns the contract the synthesis stage must match: findings, open questions
// and pre-existing issues, each output carrying the input ids it merged so Go can derive the
// sources and lenses unions instead of trusting the model with attribution.
func SynthesisSchema() json.RawMessage { return slices.Clone(synthesisSchema) }

// VerifySchema returns the contract the verification stage must match: a verdict per finding,
// plus the fields a refined verdict may rewrite. This is the one schema carrying a verdict,
// because producing one is what the stage is for.
func VerifySchema() json.RawMessage { return slices.Clone(verifySchema) }
