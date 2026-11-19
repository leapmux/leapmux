package agenttest

import "github.com/leapmux/leapmux/internal/worker/bgtask"

func (sink *Sink) UpdateBackgroundTaskDescription(rowKey, description string) error {
	root := sink.registry()
	root.bgTasksMu.Lock()
	defer root.bgTasksMu.Unlock()
	key := bgtask.NormalizeRowKey(rowKey)
	if item, exists := root.bgTasks[key]; exists {
		item.Description = (bgtask.Upsert{Description: description}).Clean().Description
		root.bgTasks[key] = item
	}
	return nil
}

func (nop) UpdateBackgroundTaskDescription(string, string) error { return nil }
