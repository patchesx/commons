package pipeline

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// EvaluateFilter returns true if the data map satisfies the filter condition.
// On any resolution error (config lookup failure, type mismatch) it fails open
// (returns true) so pipeline runs aren't silently dropped due to infrastructure issues.
func EvaluateFilter(ctx context.Context, pool *pgxpool.Pool, encKey []byte, data map[string]any, f store.WebhookFilter) bool {
	val, valExists := data[f.Field]
	fieldPresent := valExists && val != nil

	// exists/not_exists don't need a comparison value.
	switch f.Operator {
	case "exists":
		return fieldPresent
	case "not_exists":
		return !fieldPresent
	}

	// Resolve comparison value from config_store or literal.
	var compareStr string
	if f.ConfigRef != nil && *f.ConfigRef != "" {
		parts := strings.SplitN(*f.ConfigRef, ".", 2)
		if len(parts) != 2 {
			log.Printf("pipeline/filter: invalid config_ref %q — skipping filter (pass)", *f.ConfigRef)
			return true
		}
		var err error
		compareStr, err = store.GetServiceConfig(ctx, pool, parts[0], parts[1], encKey)
		if err != nil {
			log.Printf("pipeline/filter: resolve config_ref %q: %v — skipping filter (pass)", *f.ConfigRef, err)
			return true
		}
	} else if f.Value != nil {
		compareStr = *f.Value
	} else {
		return true // no value to compare against
	}

	if !fieldPresent {
		return false // field missing — comparison fails
	}

	// Numeric operators.
	switch f.Operator {
	case "gt", "gte", "lt", "lte":
		dataNum, err := toFloat64(val)
		if err != nil {
			log.Printf("pipeline/filter: field %q value %v not numeric for operator %s — failing filter", f.Field, val, f.Operator)
			return false
		}
		cmpNum, err := strconv.ParseFloat(compareStr, 64)
		if err != nil {
			log.Printf("pipeline/filter: compare value %q not numeric for operator %s — failing filter", compareStr, f.Operator)
			return false
		}
		if f.ValueScale != 0 && f.ValueScale != 1 {
			cmpNum *= f.ValueScale
		}
		switch f.Operator {
		case "gt":
			return dataNum > cmpNum
		case "gte":
			return dataNum >= cmpNum
		case "lt":
			return dataNum < cmpNum
		case "lte":
			return dataNum <= cmpNum
		}
	}

	// String/boolean operators — coerce data value to string.
	dataStr := fmt.Sprintf("%v", val)
	switch f.Operator {
	case "eq":
		return dataStr == compareStr
	case "neq":
		return dataStr != compareStr
	case "contains":
		return strings.Contains(dataStr, compareStr)
	case "not_contains":
		return !strings.Contains(dataStr, compareStr)
	}

	log.Printf("pipeline/filter: unknown operator %q — skipping filter (pass)", f.Operator)
	return true
}

func toFloat64(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case string:
		return strconv.ParseFloat(n, 64)
	}
	return 0, fmt.Errorf("cannot convert %T to float64", v)
}

// EvaluateCondition checks whether the data map satisfies a per-action condition.
// Unlike EvaluateFilter, it does not resolve config_ref values — conditions are
// simple field/operator/value expressions evaluated against the data map only.
// Unknown operators fail open (return true) so pipelines aren't blocked by typos.
func EvaluateCondition(data map[string]any, cond store.ActionCondition) bool {
	val, valExists := data[cond.Field]
	fieldPresent := valExists && val != nil

	switch cond.Operator {
	case "exists":
		return fieldPresent
	case "not_exists":
		return !fieldPresent
	}

	if !fieldPresent {
		return false
	}

	var compareStr string
	if cond.Value != nil {
		compareStr = *cond.Value
	} else {
		return true // no value to compare against — pass
	}

	dataStr := fmt.Sprintf("%v", val)
	switch cond.Operator {
	case "eq":
		return dataStr == compareStr
	case "neq":
		return dataStr != compareStr
	case "contains":
		return strings.Contains(dataStr, compareStr)
	case "not_contains":
		return !strings.Contains(dataStr, compareStr)
	}

	return true // unknown operator — fail open
}
