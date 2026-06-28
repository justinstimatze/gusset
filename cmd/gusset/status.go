package main

import (
	"fmt"
	"os"
	"time"

	"github.com/justinstimatze/gusset/internal/status"
)

// statusCmd reports sync status. The live model is owned by the daemon and will
// be read over the localhost WebSocket once the daemon exists; until then this
// reports honestly that nothing is running rather than fabricating peer state.
// It still renders the empty model, so "nothing configured" is shown explicitly
// — the never-sync-silently rule applies even before any peer is paired.
func statusCmd() error {
	fmt.Println("daemon: not running (no live peers to report yet)")
	fmt.Println()
	status.Render(os.Stdout, status.New().Snapshot(), time.Now().Unix())
	return nil
}
