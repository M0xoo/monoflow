package verify

import "testing"

// A broken upstream file reported by every dependent appears once, repo-relative, with the
// targets it breaks; target-local errors stay distinct.
func TestDedupeErrorsAcrossTargets(t *testing.T) {
	res := SuiteResult{Results: []TargetResult{
		{Target: "sdk", Status: "failed", Errors: []DiagnosticError{{File: "internal/attrnorm/truncate.go", Line: 35, Message: "undefined: X"}}},
		{Target: "exporters/zipkin", Status: "failed", Errors: []DiagnosticError{
			{File: "../../sdk/internal/attrnorm/truncate.go", Line: 35, Message: "undefined: X"},
			{File: "model.go", Line: 3, Message: "own error"},
		}},
		{Target: "log", Status: "passed"},
	}}
	DedupeErrors(&res, "/repo", map[string]string{"sdk": "sdk", "exporters/zipkin": "exporters/zipkin", "log": "log"}, 10)
	if len(res.Errors) != 2 {
		t.Fatalf("errors = %+v, want 2 distinct", res.Errors)
	}
	first := res.Errors[0]
	if first.File != "sdk/internal/attrnorm/truncate.go" || first.Breaks != 2 || len(first.Targets) != 2 {
		t.Fatalf("shared error = %+v", first)
	}
	if res.Errors[1].File != "exporters/zipkin/model.go" {
		t.Fatalf("own error path = %q", res.Errors[1].File)
	}
	if res.Results[1].Errors != nil || res.Results[1].ErrorCount != 2 {
		t.Fatalf("target errors not folded: %+v", res.Results[1])
	}
}
