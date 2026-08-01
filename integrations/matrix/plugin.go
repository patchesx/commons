package matrix

import (
	"context"
	"log"
	"time"

	"commons/plugin"
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

	pctx.RegisterAuthRoute("GET", "/api/matrix/rooms", HandleListMatrixRooms())

	pctx.RegisterScheduledJob(
		"matrix_user_sync_enabled", "matrix_user_sync_interval_minutes",
		1*time.Minute,
		func() { SyncAllUsers(context.Background(), pool, encKey) },
	)

	go StartSync(context.Background(), pool, encKey)

	log.Printf("matrix/plugin: initialized")
	return nil
}
