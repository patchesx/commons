package blocks

import (
	"fmt"

	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// ResourceBlocks returns Block Kit blocks for a list of resources.
// Each resource is a section with a "View" button that opens the URL externally.
func ResourceBlocks(resources []store.Resource) []slacklib.Block {
	if len(resources) == 0 {
		return []slacklib.Block{
			slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No resources in this category._", false, false),
				nil, nil,
			),
		}
	}

	var blks []slacklib.Block
	for i := range resources {
		r := &resources[i]

		text := fmt.Sprintf("*%s*", r.Title)
		if r.Description != nil && *r.Description != "" {
			text += "\n" + *r.Description
		}

		viewBtn := &slacklib.ButtonBlockElement{
			Type:     slacklib.METButton,
			Text:     slacklib.NewTextBlockObject(slacklib.PlainTextType, "View", false, false),
			ActionID: "open_resource_" + r.ID,
			URL:      r.URL,
		}

		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
			nil,
			slacklib.NewAccessory(viewBtn),
		))
	}
	return blks
}
