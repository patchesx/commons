package pipeline

import (
	"testing"

	"commons/store"
)

func TestEvaluateCondition_Exists(t *testing.T) {
	data := map[string]any{"user_id": "U123"}
	cond := store.ActionCondition{Field: "user_id", Operator: "exists"}
	if !EvaluateCondition(data, cond) {
		t.Error("exists should return true for present field")
	}
}

func TestEvaluateCondition_NotExists(t *testing.T) {
	data := map[string]any{"user_id": "U123"}
	cond := store.ActionCondition{Field: "missing_field", Operator: "not_exists"}
	if !EvaluateCondition(data, cond) {
		t.Error("not_exists should return true for missing field")
	}
}

func TestEvaluateCondition_Eq(t *testing.T) {
	data := map[string]any{"status": "active"}
	val := "active"
	cond := store.ActionCondition{Field: "status", Operator: "eq", Value: &val}
	if !EvaluateCondition(data, cond) {
		t.Error("eq should return true for matching value")
	}

	val = "invited"
	cond.Value = &val
	if EvaluateCondition(data, cond) {
		t.Error("eq should return false for non-matching value")
	}
}

func TestEvaluateCondition_Neq(t *testing.T) {
	data := map[string]any{"status": "active"}
	val := "invited"
	cond := store.ActionCondition{Field: "status", Operator: "neq", Value: &val}
	if !EvaluateCondition(data, cond) {
		t.Error("neq should return true for different value")
	}
}

func TestEvaluateCondition_Contains(t *testing.T) {
	data := map[string]any{"topic": "Board Meeting"}
	val := "Board"
	cond := store.ActionCondition{Field: "topic", Operator: "contains", Value: &val}
	if !EvaluateCondition(data, cond) {
		t.Error("contains should return true for substring match")
	}
}

func TestEvaluateCondition_NotContains(t *testing.T) {
	data := map[string]any{"topic": "Board Meeting"}
	val := "Test"
	cond := store.ActionCondition{Field: "topic", Operator: "not_contains", Value: &val}
	if !EvaluateCondition(data, cond) {
		t.Error("not_contains should return true when substring not found")
	}
}

func TestEvaluateCondition_MissingField_Eq(t *testing.T) {
	data := map[string]any{}
	val := "active"
	cond := store.ActionCondition{Field: "status", Operator: "eq", Value: &val}
	if EvaluateCondition(data, cond) {
		t.Error("eq should return false for missing field")
	}
}

func TestEvaluateCondition_UnknownOperator(t *testing.T) {
	data := map[string]any{"status": "active"}
	cond := store.ActionCondition{Field: "status", Operator: "unknown"}
	if !EvaluateCondition(data, cond) {
		t.Error("unknown operator should fail open (return true)")
	}
}

func TestEvaluateCondition_NilValue(t *testing.T) {
	data := map[string]any{"status": "active"}
	cond := store.ActionCondition{Field: "status", Operator: "eq", Value: nil}
	if !EvaluateCondition(data, cond) {
		t.Error("nil value should pass (no comparison value)")
	}
}

func TestEvaluateCondition_BooleanField(t *testing.T) {
	data := map[string]any{"has_profile": true}
	val := "true"
	cond := store.ActionCondition{Field: "has_profile", Operator: "eq", Value: &val}
	if !EvaluateCondition(data, cond) {
		t.Error("eq should match boolean true as string 'true'")
	}
}
