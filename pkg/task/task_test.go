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
