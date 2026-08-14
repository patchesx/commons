package admintempl

import (
	"encoding/json"
	"fmt"

	"commons/plugin"
	"commons/store"
)

type WHChannel struct {
	ID   string
	Name string
}

type WHUser struct {
	ID          string
	DisplayName string
	SlackID     string
}

func whChannelName(channels []WHChannel, id string) string {
	for _, c := range channels {
		if c.ID == id {
			return c.Name
		}
	}
	return id
}

func whUserName(users []WHUser, slackID string) string {
	for _, u := range users {
		if u.SlackID == slackID {
			return u.DisplayName
		}
	}
	return slackID
}

func whActionLabel(actionType string, types []plugin.ActionTypeInfo) string {
	for _, t := range types {
		if t.ID == actionType {
			return t.Label
		}
	}
	return actionType
}

func whActionSummary(action store.WebhookAction, types []plugin.ActionTypeInfo, channels []WHChannel, users []WHUser) string {
	switch action.Type {
	case "slack.channel":
		if id, ok := action.Params["channel_id"].(string); ok && id != "" {
			return "#" + whChannelName(channels, id)
		}
		return "No channel set"
	case "slack.dm":
		if uid, ok := action.Params["user_id"].(string); ok && uid != "" {
			return "DM → " + whUserName(users, uid)
		}
		return "No user set"
	case "resource.create":
		if cat, ok := action.Params["category"].(string); ok && cat != "" {
			return "Category: " + cat
		}
		return "No category set"
	}
	return whActionLabel(action.Type, types)
}

func whOperatorLabel(op string) string {
	switch op {
	case "eq":
		return "="
	case "neq":
		return "≠"
	case "gt":
		return ">"
	case "gte":
		return "≥"
	case "lt":
		return "<"
	case "lte":
		return "≤"
	case "contains":
		return "contains"
	case "not_contains":
		return "doesn't contain"
	case "exists":
		return "exists"
	case "not_exists":
		return "doesn't exist"
	}
	return op
}

func whFilterFieldLabel(field string, schema []plugin.DataFieldDef) string {
	for _, d := range schema {
		if d.Key == field {
			return d.Label
		}
	}
	return field
}

func whFilterSummaryValue(f store.WebhookFilter) string {
	if f.Operator == "exists" || f.Operator == "not_exists" {
		return ""
	}
	if f.ConfigRef != nil {
		s := "config:" + *f.ConfigRef
		if f.ValueScale != 0 && f.ValueScale != 1 {
			s += fmt.Sprintf(" ×%.0f", f.ValueScale)
		}
		return s
	}
	if f.Value != nil {
		return *f.Value
	}
	return ""
}

func whParamValue(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	if v, ok := params[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func whParamVariants(params map[string]any) []string {
	if params == nil {
		return nil
	}
	if vs, ok := params["message_variants"].([]any); ok && len(vs) > 0 {
		result := make([]string, 0, len(vs))
		for _, v := range vs {
			if s, ok := v.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	if s, ok := params["message_template"].(string); ok && s != "" {
		return []string{s}
	}
	return nil
}

func variantAlpineData(variants []string) string {
	if len(variants) == 0 {
		variants = []string{""}
	}
	b, _ := json.Marshal(variants)
	return `{"variants":` + string(b) + `}`
}

func hasVar(vars []plugin.DataFieldDef, key string) bool {
	for _, v := range vars {
		if v.Key == key {
			return true
		}
	}
	return false
}

func whManagedLabel(wh store.Webhook, labels map[string]string) string {
	if wh.ManagedBy == nil || *wh.ManagedBy == "" {
		return ""
	}
	if l, ok := labels[*wh.ManagedBy]; ok {
		return l
	}
	return *wh.ManagedBy
}

func scheduledManagedLabel(st store.ScheduledTrigger, labels map[string]string) string {
	if st.ManagedBy == nil || *st.ManagedBy == "" {
		return ""
	}
	if l, ok := labels[*st.ManagedBy]; ok {
		return l
	}
	return *st.ManagedBy
}

func whConditionField(cond *store.ActionCondition) string {
	if cond == nil {
		return ""
	}
	return cond.Field
}

func whConditionOp(cond *store.ActionCondition) string {
	if cond == nil {
		return ""
	}
	return cond.Operator
}

func whConditionValue(cond *store.ActionCondition) string {
	if cond == nil || cond.Value == nil {
		return ""
	}
	return *cond.Value
}

func whRetryMaxAttempts(cfg *store.RetryConfig) string {
	if cfg == nil || cfg.MaxAttempts == 0 {
		return ""
	}
	return fmt.Sprintf("%d", cfg.MaxAttempts)
}

func whRetryBackoff(cfg *store.RetryConfig) string {
	if cfg == nil {
		return ""
	}
	return cfg.Backoff
}

func whRetryInitialDelay(cfg *store.RetryConfig) string {
	if cfg == nil {
		return ""
	}
	return cfg.InitialDelay
}

func whRetryMaxDelay(cfg *store.RetryConfig) string {
	if cfg == nil {
		return ""
	}
	return cfg.MaxDelay
}

func whTimeoutSeconds(timeout *int) string {
	if timeout == nil {
		return ""
	}
	return fmt.Sprintf("%d", *timeout)
}

func whProcessorSchema(procType *string, procs []plugin.ProcessorInfo) []plugin.DataFieldDef {
	if procType == nil {
		return nil
	}
	for _, p := range procs {
		if p.Type == *procType {
			return p.DataSchema
		}
	}
	return nil
}

func whFindActionType(id string, types []plugin.ActionTypeInfo) *plugin.ActionTypeInfo {
	for i := range types {
		if types[i].ID == id {
			return &types[i]
		}
	}
	if len(types) > 0 {
		return &types[0]
	}
	return nil
}

func whActionsByRunOn(actions []store.WebhookAction, runOn string) []store.WebhookAction {
	var out []store.WebhookAction
	for _, a := range actions {
		if a.RunOn == runOn {
			out = append(out, a)
		}
	}
	return out
}

func whSuccessActionCount(actions []store.WebhookAction) int {
	n := 0
	for _, a := range actions {
		if a.RunOn == "" || a.RunOn == "success" {
			n++
		}
	}
	return n
}

func filterFormAlpineData(schema []plugin.DataFieldDef, field, op, val string) string {
	schemaJSON, _ := json.Marshal(schema)
	fieldJSON, _ := json.Marshal(field)
	opJSON, _ := json.Marshal(op)
	valJSON, _ := json.Marshal(val)
	return fmt.Sprintf(`{
  field: %s,
  operator: %s,
  value: %s,
  schema: %s,
  get fieldType() {
    var d = this.schema.find(function(d) { return d.key === this.field }.bind(this));
    return d ? d.type : 'string';
  },
  get operators() {
    var t = this.fieldType;
    if (t === 'number') return [
      {value:'eq',label:'='},{value:'neq',label:'≠'},
      {value:'gt',label:'>'},{value:'gte',label:'≥'},
      {value:'lt',label:'<'},{value:'lte',label:'≤'},
      {value:'exists',label:'exists'},{value:'not_exists',label:'does not exist'}
    ];
    if (t === 'boolean') return [
      {value:'eq',label:'='},{value:'neq',label:'≠'},
      {value:'exists',label:'exists'},{value:'not_exists',label:'does not exist'}
    ];
    return [
      {value:'eq',label:'='},{value:'neq',label:'≠'},
      {value:'contains',label:'contains'},{value:'not_contains',label:'does not contain'},
      {value:'exists',label:'exists'},{value:'not_exists',label:'does not exist'}
    ];
  },
  get needsValue() { return this.operator !== 'exists' && this.operator !== 'not_exists'; }
}`,
		string(fieldJSON), string(opJSON), string(valJSON), string(schemaJSON))
}

func whFilterInitialField(f *store.WebhookFilter) string {
	if f == nil {
		return ""
	}
	return f.Field
}

func whFilterInitialOp(f *store.WebhookFilter) string {
	if f == nil {
		return "eq"
	}
	return f.Operator
}

func whFilterInitialVal(f *store.WebhookFilter) string {
	if f == nil {
		return ""
	}
	if f.Value != nil {
		return *f.Value
	}
	return ""
}
