package eventjournal

import leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

// Tab identifies one source whose facts the Worker publishes.
type Tab struct {
	Type leapmuxv1.TabType
	ID   string
}

func (t Tab) Valid() bool {
	return t.ID != "" && (t.Type == leapmuxv1.TabType_TAB_TYPE_AGENT || t.Type == leapmuxv1.TabType_TAB_TYPE_TERMINAL)
}
