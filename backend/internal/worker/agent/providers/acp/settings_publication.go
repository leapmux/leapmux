package acp

import (
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// queueSettingsRefreshLocked reserves the complete publication order under Mu.
// Flush settingsPublications after releasing Mu. Reentrant calls append behind
// the callback that triggered them and let its drainer publish the newer state.
func (b *Base) queueSettingsRefreshLocked(refresh optionmap.Map) {
	sessionID := b.sessionID
	b.settingsPublications.Append(agent.OutputWrite{
		Label: "persist ACP settings refresh",
		Run: func() error {
			b.Mu.Lock()
			current := b.sessionID == sessionID
			b.Mu.Unlock()
			if current {
				b.sink.PersistSettingsRefresh(refresh)
			}
			return nil
		},
	})
}
