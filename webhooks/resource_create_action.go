package webhooks

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
	"commons/store"
)

// CreateResourceAction implements plugin.ActionType for "resource.create".
// It inserts a resource_library row using pipeline data as inputs.
type CreateResourceAction struct {
	pool *pgxpool.Pool
}

func NewCreateResourceAction(pool *pgxpool.Pool) *CreateResourceAction {
	return &CreateResourceAction{pool: pool}
}

func (a *CreateResourceAction) ID() string                          { return "resource.create" }
func (a *CreateResourceAction) Label() string                       { return "Create Library Resource" }
func (a *CreateResourceAction) RequiredCapabilities() []string      { return nil }
func (a *CreateResourceAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *CreateResourceAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "title", Label: "Title", Type: "text", Required: true, Dynamic: true,
			Description: "Resource title. Supports {{key}} references."},
		{Key: "url", Label: "URL", Type: "text", Required: true, Dynamic: true,
			Description: "Resource URL. Supports {{key}} references, e.g. {{drive_folder_url}}."},
		{Key: "category", Label: "Category", Type: "resource_category_select", Required: true,
			Description: "Must match an existing resource category name."},
		{Key: "description", Label: "Description", Type: "text", Dynamic: true,
			Description: "Optional description. Supports {{key}} references."},
	}
}

func (a *CreateResourceAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	title, _ := params["title"].(string)
	url, _ := params["url"].(string)
	category, _ := params["category"].(string)
	if title == "" {
		return nil, fmt.Errorf("resource.create: title is required")
	}
	if url == "" {
		return nil, fmt.Errorf("resource.create: url is required")
	}
	if category == "" {
		return nil, fmt.Errorf("resource.create: category is required")
	}
	var desc *string
	if d, ok := params["description"].(string); ok && d != "" {
		desc = &d
	}
	r := &store.Resource{
		Title:       title,
		URL:         url,
		Category:    category,
		Description: desc,
	}
	if err := store.CreateResource(ctx, a.pool, r); err != nil {
		return nil, fmt.Errorf("resource.create: %w", err)
	}
	return nil, nil
}
