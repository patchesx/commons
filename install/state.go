package install

import (
	"encoding/hex"

	"github.com/jackc/pgx/v5/pgxpool"
)

// State holds in-memory install context for the duration of the wizard.
type State struct {
	Pool          *pgxpool.Pool
	EncryptionKey string // hex-encoded 64-char string; populated from cfg.EncryptionKey in main.go
	DevMode       bool
}

// EncryptionKeyBytes decodes EncryptionKey from hex to a 32-byte slice
// for use with store.Encrypt / store.Decrypt.
func (s *State) EncryptionKeyBytes() []byte {
	b, _ := hex.DecodeString(s.EncryptionKey)
	return b
}
