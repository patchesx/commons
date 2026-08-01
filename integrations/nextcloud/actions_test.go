package nextcloud

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/plugin"
)

func TestSaveFileActionMissingFolderID(t *testing.T) {
	a := &SaveFileAction{}
	_, err := a.Execute(context.Background(), map[string]any{}, plugin.NoopActionContext{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "folder_id is required")
}

func TestSaveFileActionIDAndLabel(t *testing.T) {
	a := &SaveFileAction{}
	assert.Equal(t, "nextcloud.save_file", a.ID())
	assert.Equal(t, "Save to Nextcloud", a.Label())
	assert.Equal(t, []string{"nextcloud.storage"}, a.RequiredCapabilities())
}

func TestSaveFileActionParamSchema(t *testing.T) {
	a := &SaveFileAction{}
	schema := a.ParamSchema()
	keys := make([]string, len(schema))
	for i, p := range schema {
		keys[i] = p.Key
	}
	assert.Contains(t, keys, "folder_id")
	assert.Contains(t, keys, "all_files")
	assert.Contains(t, keys, "meeting_topic")
	assert.Contains(t, keys, "meeting_date")
}
