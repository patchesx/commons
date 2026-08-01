package solidaritytech

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
	"commons/store"
)

const providerSolidarityTech = "solidaritytech"

// LookupUserAction implements plugin.ActionType for "solidaritytech.lookup_user".
// It resolves a member from a provider identity (e.g. slack + {{user_id}}), looks up the
// member's SolidarityTech profile by email, caches the SolidarityTech profile id on the
// member as a "solidaritytech" identity, and emits profile fields for downstream steps.
type LookupUserAction struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (a *LookupUserAction) ID() string                     { return "solidaritytech.lookup_user" }
func (a *LookupUserAction) Label() string                  { return "Look up SolidarityTech profile" }
func (a *LookupUserAction) RequiredCapabilities() []string { return []string{"solidaritytech.api"} }
func (a *LookupUserAction) OutputSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "solidaritytech_user_id", Label: "SolidarityTech User ID", Type: "string"},
		{Key: "solidaritytech_hash_id", Label: "SolidarityTech Hash ID", Type: "string"},
		{Key: "solidaritytech_email", Label: "SolidarityTech Email", Type: "string"},
		{Key: "solidaritytech_first_name", Label: "SolidarityTech First Name", Type: "string"},
		{Key: "solidaritytech_last_name", Label: "SolidarityTech Last Name", Type: "string"},
		{Key: "member_id", Label: "Member ID", Type: "string"},
		{Key: "member_email", Label: "Member Email", Type: "string"},
	}
}
func (a *LookupUserAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "provider", Label: "Member Identity Provider", Type: "select", Required: true,
			Description: "The provider identity used to resolve the member record whose SolidarityTech profile you want to look up.",
			Options: []plugin.SelectOption{
				{Value: "slack", Label: "Slack"},
				{Value: "discord", Label: "Discord"},
				{Value: "matrix", Label: "Matrix"},
				{Value: "web", Label: "Web (email)"},
			}},
		{Key: "external_id", Label: "External ID", Type: "text", Required: true, Dynamic: true,
			Description: "The member's external id for the chosen provider, e.g. {{user_id}} from a Slack team_join trigger."},
	}
}

func (a *LookupUserAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	provider, _ := params["provider"].(string)
	externalID, _ := params["external_id"].(string)
	if provider == "" {
		return nil, fmt.Errorf("solidaritytech.lookup_user: provider is required")
	}
	if externalID == "" {
		return nil, fmt.Errorf("solidaritytech.lookup_user: external_id is required")
	}

	client, err := NewClient(ctx, a.pool, a.encKey)
	if err != nil {
		return nil, fmt.Errorf("solidaritytech.lookup_user: %w", err)
	}

	// Resolve the member from the chosen provider identity.
	member, err := store.GetUserByExternalID(ctx, a.pool, provider, externalID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("solidaritytech.lookup_user: no member found for %s identity %q", provider, externalID)
		}
		return nil, fmt.Errorf("solidaritytech.lookup_user: resolve member: %w", err)
	}

	memberEmail := ""
	if member.Email != nil {
		memberEmail = *member.Email
	}

	// Fast path: the SolidarityTech profile id is already cached on the member.
	var stUser *User
	if cachedID, err := store.GetUserExternalID(ctx, a.pool, member.ID, providerSolidarityTech); err == nil && cachedID != "" {
		id, parseErr := strconv.Atoi(cachedID)
		if parseErr == nil {
			if u, getErr := client.GetUserByID(ctx, id); getErr == nil {
				stUser = u
			}
		}
	}

	// Slow path: look the profile up by the member's email and cache the mapping.
	if stUser == nil {
		if memberEmail == "" {
			return nil, fmt.Errorf("solidaritytech.lookup_user: member %s has no email to look up a SolidarityTech profile", member.ID)
		}
		u, err := client.GetUserByEmail(ctx, memberEmail)
		if err != nil {
			if errors.Is(err, ErrUserNotFound) {
				return nil, fmt.Errorf("solidaritytech.lookup_user: no SolidarityTech profile for %s", memberEmail)
			}
			return nil, fmt.Errorf("solidaritytech.lookup_user: %w", err)
		}
		stUser = u
		if linkErr := store.LinkIdentity(ctx, a.pool, member.ID, providerSolidarityTech,
			strconv.Itoa(stUser.ID), stUser.FirstName+" "+stUser.LastName, stUser.Email); linkErr != nil {
			return nil, fmt.Errorf("solidaritytech.lookup_user: cache profile mapping: %w", linkErr)
		}
	}

	return map[string]any{
		"solidaritytech_user_id":    strconv.Itoa(stUser.ID),
		"solidaritytech_hash_id":    stUser.HashID,
		"solidaritytech_email":      stUser.Email,
		"solidaritytech_first_name": stUser.FirstName,
		"solidaritytech_last_name":  stUser.LastName,
		"member_id":                 member.ID,
		"member_email":              memberEmail,
	}, nil
}

// SetCustomPropertyAction implements plugin.ActionType for "solidaritytech.set_custom_property".
// It sets a custom property value on a SolidarityTech profile via PUT /users/{id}.
type SetCustomPropertyAction struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (a *SetCustomPropertyAction) ID() string    { return "solidaritytech.set_custom_property" }
func (a *SetCustomPropertyAction) Label() string { return "Set SolidarityTech custom property" }
func (a *SetCustomPropertyAction) RequiredCapabilities() []string {
	return []string{"solidaritytech.api"}
}
func (a *SetCustomPropertyAction) OutputSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "solidaritytech_user_id", Label: "SolidarityTech User ID", Type: "string"},
	}
}
func (a *SetCustomPropertyAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "user_id", Label: "SolidarityTech User ID", Type: "text", Required: true, Dynamic: true,
			Description: "The SolidarityTech profile id to update, e.g. {{solidaritytech_user_id}} from a Look up SolidarityTech profile step."},
		{Key: "property_key", Label: "Property Key", Type: "text", Required: true,
			Description: "The internal_name of the custom property to set, e.g. \"slack_user_id\"."},
		{Key: "value", Label: "Value", Type: "text", Required: true, Dynamic: true,
			Description: "The value to set. Supports {{key}} references, e.g. {{user_id}}."},
		{Key: "append", Label: "Append (merge) for checkboxes", Type: "boolean", Default: false,
			Description: "For Multiple-Checkboxes properties: merge the value with existing values (true) or replace them (false). No effect on single-value fields."},
	}
}

func (a *SetCustomPropertyAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	userIDStr, _ := params["user_id"].(string)
	propertyKey, _ := params["property_key"].(string)
	value, _ := params["value"].(string)
	appendMode, _ := params["append"].(bool)

	if userIDStr == "" {
		return nil, fmt.Errorf("solidaritytech.set_custom_property: user_id is required")
	}
	if propertyKey == "" {
		return nil, fmt.Errorf("solidaritytech.set_custom_property: property_key is required")
	}
	if value == "" {
		return nil, fmt.Errorf("solidaritytech.set_custom_property: value is required")
	}

	id, err := strconv.Atoi(userIDStr)
	if err != nil {
		return nil, fmt.Errorf("solidaritytech.set_custom_property: user_id %q is not a number", userIDStr)
	}

	client, err := NewClient(ctx, a.pool, a.encKey)
	if err != nil {
		return nil, fmt.Errorf("solidaritytech.set_custom_property: %w", err)
	}

	props := map[string]any{propertyKey: value}
	if err := client.UpdateUserCustomProperties(ctx, id, props, appendMode); err != nil {
		return nil, fmt.Errorf("solidaritytech.set_custom_property: %w", err)
	}

	return map[string]any{"solidaritytech_user_id": userIDStr}, nil
}
