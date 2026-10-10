package task

import (
	"testing"
)

func TestCheckBoundaries(t *testing.T) {
	bundle := &TaskBundle{
		TaskID:        "task-billing",
		AllowedPaths:  []string{"services/billing/**"},
		ReadOnlyPaths: []string{"contracts/**"},
	}

	// 1. Valid modification
	res1 := CheckBoundaries(bundle, []string{"services/billing/tax.ts", "services/billing/tax.test.ts"})
	if !res1.Valid {
		t.Fatalf("expected valid boundaries, got violations: %v", res1.Violations)
	}

	// 2. Read-only violation
	res2 := CheckBoundaries(bundle, []string{"services/billing/tax.ts", "contracts/schema.proto"})
	if res2.Valid || len(res2.Violations) == 0 {
		t.Fatalf("expected read-only violation for contracts/schema.proto")
	}

	// 3. Out-of-bounds package modification
	res3 := CheckBoundaries(bundle, []string{"services/auth/auth.go"})
	if res3.Valid || len(res3.Violations) == 0 {
		t.Fatalf("expected out-of-bounds violation for services/auth/auth.go")
	}
}

func TestSliceStackedDiff(t *testing.T) {
	files := []string{
		"contracts/schema.proto",
		"services/billing/types.ts",
		"services/billing/tax.ts",
		"services/billing/tax.test.ts",
	}

	slice := SliceStackedDiff(files)

	if len(slice.TypesAndContracts) != 2 {
		t.Errorf("expected 2 type files, got %d: %v", len(slice.TypesAndContracts), slice.TypesAndContracts)
	}
	if len(slice.CoreImplementation) != 1 {
		t.Errorf("expected 1 impl file, got %d: %v", len(slice.CoreImplementation), slice.CoreImplementation)
	}
	if len(slice.TestsAndConsumers) != 1 {
		t.Errorf("expected 1 test file, got %d: %v", len(slice.TestsAndConsumers), slice.TestsAndConsumers)
	}
}

func TestCheckBoundariesIgnoresInternalArtifacts(t *testing.T) {
	bundle := &TaskBundle{
		AllowedPaths:  []string{"services/**", "packages/**"},
		ReadOnlyPaths: []string{"contracts/**"},
	}

	internal := []string{
		".agents/circuit_breaker.json",
		".agents/checkpoints.json",
		".agents/events/evt_1.json",
		".agents/dist/core/manifest.json",
		".agents/cache/index.json",
		".gitignore",
		".git/index",
	}
	res := CheckBoundaries(bundle, append(internal, "services/billing/tax.ts"))
	if !res.Valid {
		t.Fatalf("expected internal MonoFlow artifacts to be ignored, got violations: %v", res.Violations)
	}

	// The task contract itself is never internal.
	res = CheckBoundaries(bundle, []string{".agents/task.json"})
	if res.Valid {
		t.Fatalf("expected .agents/task.json modification to be flagged")
	}

	// Explicit read-only declarations still win over the internal exemption.
	strict := &TaskBundle{AllowedPaths: []string{"services/**"}, ReadOnlyPaths: []string{".gitignore"}}
	res = CheckBoundaries(strict, []string{".gitignore"})
	if res.Valid {
		t.Fatalf("expected read-only .gitignore modification to be flagged")
	}
}
