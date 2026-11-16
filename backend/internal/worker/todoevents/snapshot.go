package todoevents

import "fmt"

// PrepareSnapshot validates the complete list and returns an owned, normalized
// copy up to MaxTodos. Invalid entries beyond the retained prefix still fail.
func PrepareSnapshot(items []Item) ([]Item, error) {
	prepared := make([]Item, 0, min(len(items), MaxTodos))
	for index, item := range items {
		item.Status = item.Status.OrPending()
		switch item.Status {
		case StatusPending, StatusInProgress, StatusCompleted, StatusDeleted, StatusBlocked:
		default:
			return nil, fmt.Errorf("todo item %d has invalid status %d", index+1, item.Status)
		}
		if len(prepared) < MaxTodos {
			prepared = append(prepared, item)
		}
	}
	return prepared, nil
}
