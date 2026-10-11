package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// indexedRaw bundles a message's original index, raw bytes, and (optional)
// classification so downstream sort-and-emit can reconstruct the persisted
// thread in original order. idx == -1 marks an empty slot.
type indexedRaw struct {
	idx  int
	raw  json.RawMessage
	kind agent.NotificationKind
}

type reductionRaw struct {
	indexedRaw
	providerKey string
}

// notificationSettingChange preserves visible fields while it reduces scalar changes.
type notificationSettingChange struct {
	old    string
	new    string
	fields map[string]json.RawMessage
}

func (change *notificationSettingChange) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	var old, new string
	if value, present := fields["old"]; present {
		if err := json.Unmarshal(value, &old); err != nil {
			return err
		}
	}
	if value, present := fields["new"]; present {
		if err := json.Unmarshal(value, &new); err != nil {
			return err
		}
	}
	*change = notificationSettingChange{old: old, new: new, fields: fields}
	return nil
}

func (change notificationSettingChange) MarshalJSON() ([]byte, error) {
	fields := maps.Clone(change.fields)
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	fields["old"], _ = json.Marshal(change.old)
	fields["new"], _ = json.Marshal(change.new)
	return json.Marshal(fields)
}

func (change notificationSettingChange) keepOriginal(original notificationSettingChange) notificationSettingChange {
	fields := maps.Clone(change.fields)
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	if change.old != original.old {
		delete(fields, "old_label")
	}
	if label, present := original.fields["old_label"]; present {
		fields["old_label"] = label
	}
	change.old = original.old
	change.fields = fields
	return change
}

func reduceNotificationThread(messages []json.RawMessage, state agent.NotificationReductionState, incoming []byte, plugin agent.Provider) ([]json.RawMessage, agent.NotificationReductionState, error) {
	if plugin == nil {
		plugin = agent.ProviderDefaults{}
	}

	type envelope struct {
		Type    string                               `json:"type"`
		Subtype string                               `json:"subtype"`
		Changes map[string]notificationSettingChange `json:"changes,omitempty"`
		RLInfo  *struct {
			RateLimitType string `json:"rateLimitType"`
		} `json:"rate_limit_info,omitempty"`
	}

	// Last-by-index slots: each holds the most recent occurrence of one
	// notification class. settings is special — its raw payload is rebuilt
	// at emit time from mergedChanges so the persisted entry reflects only
	// the net effective diff across the thread.
	settings := indexedRaw{idx: -1}
	contextCleared := indexedRaw{idx: -1}
	interrupted := indexedRaw{idx: -1}
	// stop_ignored folds like interrupted: keep the latest. Every occurrence
	// says the same thing -- the accepted stop changed nothing, press again --
	// and the newest one is the one whose turn is still running.
	stopIgnored := indexedRaw{idx: -1}
	planExec := indexedRaw{idx: -1}
	planUpdated := indexedRaw{idx: -1}
	status := indexedRaw{idx: -1}
	apiRetry := indexedRaw{idx: -1}

	mergedChanges := map[string]notificationSettingChange{}
	for key, original := range state.CancelledSettings {
		mergedChanges[key] = notificationSettingChange{old: original, new: original}
	}
	providerKeys := make(map[int]string, len(state.ProviderSlots))
	for key, index := range state.ProviderSlots {
		providerKeys[index-1] = key
	}
	priorCount := len(messages)
	messages = slices.Clone(messages)
	if utf8.Valid(incoming) && json.Valid(incoming) {
		messages = append(messages, slices.Clone(incoming))
	}

	rateLimitByType := map[string]indexedRaw{}
	providerEntries := map[string]reductionRaw{}

	var keepAll []indexedRaw

	for i, raw := range messages {
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			if _, retained := providerKeys[i]; !retained {
				keepAll = append(keepAll, indexedRaw{idx: i, raw: raw})
				continue
			}
		}
		if _, retained := providerKeys[i]; retained {
			env.Type = ""
		}

		switch env.Type {
		case contracts.NotificationTypeSettingsChanged:
			for key, val := range env.Changes {
				if i < priorCount {
					if _, cancelled := state.CancelledSettings[key]; cancelled {
						return nil, agent.NotificationReductionState{}, fmt.Errorf("the stored setting %q is both visible and canceled", key)
					}
				}
				if existing, ok := mergedChanges[key]; ok {
					val = val.keepOriginal(existing)
				}
				if val.old == val.new {
					val.fields = nil
				}
				mergedChanges[key] = val
			}
			settings = indexedRaw{idx: i, raw: raw}

		case contracts.NotificationTypeContextCleared:
			contextCleared = indexedRaw{idx: i, raw: raw}
			keepAll = slices.DeleteFunc(keepAll, func(ir indexedRaw) bool {
				return ir.kind == agent.NotificationKindCompactionBoundary
			})

		case contracts.NotificationTypePlanExecution:
			planExec = indexedRaw{idx: i, raw: raw}

		case contracts.NotificationTypePlanUpdated:
			// Multiple plan_updated entries within one notification thread
			// fold to the most recent — same pattern as plan_execution. The
			// frontend extractor already prefers the latest, but keeping
			// only the most recent in the persisted thread also keeps the
			// chat readable when an agent iterates on a plan title.
			planUpdated = indexedRaw{idx: i, raw: raw}

		// goal_updated and goal_cleared have NO case here, and the omission is
		// deliberate: they fall to `default:`, where no Classify recognizes
		// them, and every entry is kept.
		//
		// Folding them to the latest -- the obvious move, because plan_updated
		// right above does exactly that -- destroys the rows this feature
		// exists to write. The applier already writes one row per TRANSITION
		// and nothing per progress report, so each surviving entry is a real
		// change, and two of them land adjacent precisely when the user drove
		// both: a goal set and then paused, or the same objective restarted
		// with a fresh created_at. Keeping only the last one reports "Goal
		// paused: X" for a goal the reader never saw arrive, and reports one
		// "Goal set: X" for a restart the transition test went out of its way
		// to detect.
		//
		// A plan title iterating toward its final wording is the opposite case,
		// which is why the two are treated differently.

		case contracts.NotificationTypeInterrupted:
			interrupted = indexedRaw{idx: i, raw: raw}

		case contracts.NotificationTypeStopIgnored:
			stopIgnored = indexedRaw{idx: i, raw: raw}

		case contracts.NotificationTypeRateLimit:
			key := "unknown"
			if env.RLInfo != nil && env.RLInfo.RateLimitType != "" {
				key = env.RLInfo.RateLimitType
			}
			rateLimitByType[key] = indexedRaw{idx: i, raw: raw}

		case contracts.NotificationTypeCompacting:
			status = indexedRaw{idx: i, raw: raw, kind: agent.NotificationKindStatus}

		default:
			class := agent.NotificationClassification{}
			if key, retained := providerKeys[i]; retained {
				class = agent.NotificationClassification{Kind: agent.NotificationKindProviderScoped, Key: key}
			} else {
				class = plugin.Classify(raw)
			}
			switch class.Kind {
			case agent.NotificationKindStatus:
				status = indexedRaw{idx: i, raw: raw, kind: class.Kind}
			case agent.NotificationKindAPIRetry:
				apiRetry = indexedRaw{idx: i, raw: raw, kind: class.Kind}
			case agent.NotificationKindCompactionBoundary:
				status = indexedRaw{idx: -1}
				if contextCleared.idx >= 0 && i > contextCleared.idx {
					contextCleared = indexedRaw{idx: -1}
				}
				keepAll = append(keepAll, indexedRaw{idx: i, raw: raw, kind: class.Kind})
			case agent.NotificationKindProviderScoped:
				prev, ok := providerEntries[class.Key]
				if ok {
					merged, err := plugin.Merge(class, prev.raw, raw)
					if err != nil {
						slog.Warn("consolidate provider notification merge failed", "key", class.Key, "error", err)
						merged = raw
					}
					providerEntries[class.Key] = reductionRaw{indexedRaw: indexedRaw{idx: i, raw: merged, kind: class.Kind}, providerKey: class.Key}
				} else {
					providerEntries[class.Key] = reductionRaw{indexedRaw: indexedRaw{idx: i, raw: raw, kind: class.Kind}, providerKey: class.Key}
				}
			default:
				keepAll = append(keepAll, indexedRaw{idx: i, raw: raw})
			}
		}
	}

	var entries []reductionRaw

	// Settings is rebuilt at emit time so the persisted payload reflects only
	// effective net changes; intermediate flips that cancel out are dropped.
	if settings.idx >= 0 {
		effective := map[string]notificationSettingChange{}
		for key, val := range mergedChanges {
			if val.old != val.new {
				effective[key] = val
			}
		}
		if len(effective) > 0 {
			var entry map[string]json.RawMessage
			if err := json.Unmarshal(settings.raw, &entry); err != nil {
				return nil, agent.NotificationReductionState{}, err
			}
			changes, err := json.Marshal(effective)
			if err != nil {
				return nil, agent.NotificationReductionState{}, err
			}
			entry[contracts.NotificationFieldChanges] = changes
			// A context clear uses its own notification, separate from a settings change.
			delete(entry, "contextCleared")
			data, err := json.Marshal(entry)
			if err != nil {
				return nil, agent.NotificationReductionState{}, err
			}
			entries = append(entries, reductionRaw{indexedRaw: indexedRaw{idx: settings.idx, raw: data}})
		}
	}

	for _, slot := range []indexedRaw{contextCleared, planExec, planUpdated, interrupted, stopIgnored, status, apiRetry} {
		if slot.idx >= 0 {
			entries = append(entries, reductionRaw{indexedRaw: slot})
		}
	}

	for _, rateLimit := range rateLimitByType {
		entries = append(entries, reductionRaw{indexedRaw: rateLimit})
	}

	for _, providerEntry := range providerEntries {
		entries = append(entries, providerEntry)
	}

	for _, entry := range keepAll {
		entries = append(entries, reductionRaw{indexedRaw: entry})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].idx < entries[j].idx
	})

	result := make([]json.RawMessage, 0, len(entries))
	updated := agent.NotificationReductionState{ProviderSlots: make(map[string]int), CancelledSettings: make(map[string]string)}
	for key, change := range mergedChanges {
		if change.old == change.new {
			updated.CancelledSettings[key] = change.old
		}
	}
	for _, entry := range entries {
		result = append(result, entry.raw)
		if entry.kind == agent.NotificationKindProviderScoped {
			updated.ProviderSlots[entry.providerKey] = len(result)
		}
	}

	if len(result) == 0 {
		return []json.RawMessage{}, updated, nil
	}

	return result, updated, nil
}
