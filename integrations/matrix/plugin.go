package matrix

import (
	"context"
	"log"

	"commons/plugin"
	"commons/store"
)

type MatrixPlugin struct{}

func init() { plugin.Register(&MatrixPlugin{}) }

func (p *MatrixPlugin) Name() string    { return "matrix" }
func (p *MatrixPlugin) Label() string   { return "Matrix" }
func (p *MatrixPlugin) Version() string { return "1.0.0" }
func (p *MatrixPlugin) Provides() []string {
	return []string{"matrix.notify", "matrix.commands"}
}
func (p *MatrixPlugin) Migrations() []plugin.Migration { return nil }

func (p *MatrixPlugin) Init(pctx plugin.PluginContext) error {
	pool := pctx.DB()
	encKey := pctx.EncKey()

	Init(pool, encKey)

	pctx.AddNotifier(NewNotifier(pool))

	plugin.RegisterActionType(&RoomMessageAction{})
	plugin.RegisterActionType(&DirectMessageAction{})
	plugin.RegisterActionType(&SyncMembersAction{pool: pool, encKey: encKey})

	pctx.RegisterAuthRoute("GET", "/api/matrix/rooms", HandleListMatrixRooms())

	// Seed managed scheduled trigger for member sync.
	st, err := store.UpsertManagedScheduledTrigger(context.Background(), pool, store.UpsertManagedScheduledTriggerParams{
		Name:      "Matrix Member Sync",
		Schedule:  "1m",
		Timezone:  "UTC",
		ManagedBy: "matrix",
		Enabled:   true,
	})
	if err != nil {
		log.Printf("matrix/plugin: seed scheduled trigger: %v", err)
	} else {
		if err := store.EnsureWebhookAction(context.Background(), pool, st.ID, "matrix.sync_members"); err != nil {
			log.Printf("matrix/plugin: seed sync_members action: %v", err)
		}
	}

	go StartSync(context.Background(), pool, encKey)

	log.Printf("matrix/plugin: initialized")
	return nil
}
