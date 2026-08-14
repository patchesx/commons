package core

import (
	"testing"
)

func TestEvaluateOperator_Exists(t *testing.T) {
	if !evaluateOperator("hello", "exists", "") {
		t.Error("exists should return true for non-empty field")
	}
	if evaluateOperator("", "exists", "") {
		t.Error("exists should return false for empty field")
	}
}

func TestEvaluateOperator_NotExists(t *testing.T) {
	if evaluateOperator("hello", "not_exists", "") {
		t.Error("not_exists should return false for non-empty field")
	}
	if !evaluateOperator("", "not_exists", "") {
		t.Error("not_exists should return true for empty field")
	}
}

func TestEvaluateOperator_Eq(t *testing.T) {
	if !evaluateOperator("active", "eq", "active") {
		t.Error("eq should return true for matching values")
	}
	if evaluateOperator("active", "eq", "invited") {
		t.Error("eq should return false for non-matching values")
	}
}

func TestEvaluateOperator_Neq(t *testing.T) {
	if evaluateOperator("active", "neq", "active") {
		t.Error("neq should return false for matching values")
	}
	if !evaluateOperator("active", "neq", "invited") {
		t.Error("neq should return true for non-matching values")
	}
}

func TestEvaluateOperator_Contains(t *testing.T) {
	if !evaluateOperator("hello world", "contains", "world") {
		t.Error("contains should return true when substring found")
	}
	if evaluateOperator("hello", "contains", "world") {
		t.Error("contains should return false when substring not found")
	}
}

func TestEvaluateOperator_NotContains(t *testing.T) {
	if evaluateOperator("hello world", "not_contains", "world") {
		t.Error("not_contains should return false when substring found")
	}
	if !evaluateOperator("hello", "not_contains", "world") {
		t.Error("not_contains should return true when substring not found")
	}
}

func TestEvaluateOperator_Unknown(t *testing.T) {
	if !evaluateOperator("hello", "unknown_op", "world") {
		t.Error("unknown operator should fail open (return true)")
	}
}

func TestConditionAction_Execute(t *testing.T) {
	action := &ConditionAction{}
	output, err := action.Execute(nil, map[string]any{
		"field":      "12345",
		"operator":   "exists",
		"output_key": "has_id",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result, ok := output["has_id"].(bool); !ok || !result {
		t.Errorf("expected has_id=true, got %v", output["has_id"])
	}
}

func TestConditionAction_DefaultOutputKey(t *testing.T) {
	action := &ConditionAction{}
	output, err := action.Execute(nil, map[string]any{
		"field":    "",
		"operator": "exists",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result, ok := output["condition_result"].(bool); !ok || result {
		t.Errorf("expected condition_result=false, got %v", output["condition_result"])
	}
}

func TestSetVariableAction_Execute(t *testing.T) {
	action := &SetVariableAction{}
	output, err := action.Execute(nil, map[string]any{
		"name":  "my_var",
		"value": "hello",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v := output["my_var"]; v != "hello" {
		t.Errorf("expected my_var=hello, got %v", v)
	}
}

func TestSetVariableAction_MissingName(t *testing.T) {
	action := &SetVariableAction{}
	_, err := action.Execute(nil, map[string]any{
		"value": "hello",
	}, nil)
	if err == nil {
		t.Error("expected error for missing name")
	}
}
