package verify

type Scenario struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Prompt      string `json:"prompt"`
	Expected    string `json:"expected"`
	SignalType  string `json:"signal_type"`
}

var ScenariosV1 = []Scenario{
	{ID: "S001", Category: "Naming", Description: "Explicit correction: prefer named imports", SignalType: "explicit_correction", Prompt: "I asked for named imports but you used default — use named imports instead", Expected: "named imports"},
	{ID: "S002", Category: "Testing", Description: "Stated preference: always write table-driven tests", SignalType: "stated_preference", Prompt: "I prefer table-driven tests for all Go tests", Expected: "table-driven"},
	{ID: "S003", Category: "Error Handling", Description: "User diff: wrap errors with fmt.Errorf %w", SignalType: "user_diff", Prompt: "User changed error handling to wrap with %w", Expected: "wrap errors"},
	{ID: "S004", Category: "Types", Description: "Explicit prefer: interfaces over concrete types", SignalType: "explicit_prefer", Prompt: "I prefer small interfaces over concrete types in function signatures", Expected: "small interfaces"},
	{ID: "S005", Category: "General", Description: "Quiet convention: early returns", SignalType: "stated_preference", Prompt: "Use early returns instead of nested ifs", Expected: "early returns"},
	{ID: "S006", Category: "Imports", Description: "Buried decision: stdlib first in import blocks", SignalType: "user_diff", Prompt: "User reordered imports to put stdlib first", Expected: "stdlib first"},
}

const ScenariosVersion = "v1"
