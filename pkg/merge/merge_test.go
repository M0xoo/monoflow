package merge

import (
	"strings"
	"testing"
)

func TestMergeGoASTDisjointFunctions(t *testing.T) {
	base := []byte(`package service

import "fmt"

func ProcessOrder() {
	fmt.Println("processing")
}
`)

	ours := []byte(`package service

import (
	"fmt"
	"time"
)

func ProcessOrder() {
	fmt.Println("processing")
}

func CancelOrder() {
	time.Sleep(10 * time.Millisecond)
}
`)

	theirs := []byte(`package service

import (
	"fmt"
	"strings"
)

func ProcessOrder() {
	fmt.Println("processing")
}

func RefundOrder() {
	strings.TrimSpace("refund")
}
`)

	res, err := Merge3Way("service/order.go", base, ours, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.HasConflict {
		t.Fatalf("expected clean semantic merge, got conflicts: %+v", res.Conflicts)
	}

	merged := string(res.MergedContent)

	// Verify both functions are preserved
	if !strings.Contains(merged, "func CancelOrder()") {
		t.Errorf("merged result missing CancelOrder: %s", merged)
	}
	if !strings.Contains(merged, "func RefundOrder()") {
		t.Errorf("merged result missing RefundOrder: %s", merged)
	}

	// Verify imports are unioned
	if !strings.Contains(merged, `"time"`) || !strings.Contains(merged, `"strings"`) || !strings.Contains(merged, `"fmt"`) {
		t.Errorf("merged result missing imports: %s", merged)
	}
}

func TestMergeGoASTConflictSameFunction(t *testing.T) {
	base := []byte(`package service

func ProcessOrder() string {
	return "base"
}
`)

	ours := []byte(`package service

func ProcessOrder() string {
	return "ours_v2"
}
`)

	theirs := []byte(`package service

func ProcessOrder() string {
	return "theirs_v2"
}
`)

	res, err := Merge3Way("service/order.go", base, ours, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !res.HasConflict {
		t.Fatalf("expected conflict on colliding function body, but got none")
	}

	if len(res.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(res.Conflicts))
	}

	if res.Conflicts[0].Symbol != "ProcessOrder" {
		t.Errorf("expected conflict on ProcessOrder, got %s", res.Conflicts[0].Symbol)
	}
}

func TestMergeBlockSemanticTypeScript(t *testing.T) {
	base := []byte(`import { log } from './logger';

export function baseFeature() {
  log('base');
}
`)

	ours := []byte(`import { log } from './logger';
import { sendEmail } from './mailer';

export function baseFeature() {
  log('base');
}

export function notifyUser() {
  sendEmail();
}
`)

	theirs := []byte(`import { log } from './logger';
import { sendSMS } from './sms';

export function baseFeature() {
  log('base');
}

export function alertAdmin() {
  sendSMS();
}
`)

	res, err := Merge3Way("src/features.ts", base, ours, theirs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.HasConflict {
		t.Fatalf("expected clean merge, got conflicts: %+v", res.Conflicts)
	}

	merged := string(res.MergedContent)
	if !strings.Contains(merged, "notifyUser") || !strings.Contains(merged, "alertAdmin") {
		t.Errorf("expected both functions to be synthesized, got:\n%s", merged)
	}
	if !strings.Contains(merged, "sendEmail") || !strings.Contains(merged, "sendSMS") {
		t.Errorf("expected both imports to be synthesized, got:\n%s", merged)
	}
}
