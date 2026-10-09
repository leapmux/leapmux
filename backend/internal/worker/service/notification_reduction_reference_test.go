package service

import (
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"sort"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// consolidateNotificationThread consolidates a notification thread's messages.
// Service-owned LeapMux notification types are merged centrally, while
// provider-owned raw payloads are classified through the injected plugin.
// Ordering is preserved by the last occurrence index of each retained entry.
func consolidateNotificationThread(messages []json.RawMessage, plugin agent.Provider) []json.RawMessage {
	if plugin == nil {
		plugin = agent.ProviderDefaults{}
	}

	type settingsChange struct {
		Old    string
		New    string
		Fields map[string]json.RawMessage
	}

	type envelope struct {
		Type    string                     `json:"type"`
		Subtype string                     `json:"subtype"`
		Changes map[string]json.RawMessage `json:"changes,omitempty"`
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

	mergedChanges := map[string]settingsChange{}

	rateLimitByType := map[string]indexedRaw{}
	providerEntries := map[string]indexedRaw{}

	var keepAll []indexedRaw

	for i, raw := range messages {
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			slog.Warn("consolidate notification unmarshal failed", "error", err)
			keepAll = append(keepAll, indexedRaw{idx: i, raw: raw})
			continue
		}
		changes := make(map[string]settingsChange, len(env.Changes))
		validChanges := true
		for key, value := range env.Changes {
			var scalars struct {
				Old string `json:"old"`
				New string `json:"new"`
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(value, &scalars) != nil || json.Unmarshal(value, &fields) != nil {
				validChanges = false
				break
			}
			changes[key] = settingsChange{Old: scalars.Old, New: scalars.New, Fields: fields}
		}
		if !validChanges {
			keepAll = append(keepAll, indexedRaw{idx: i, raw: raw})
			continue
		}

		switch env.Type {
		case contracts.NotificationTypeSettingsChanged:
			for key, val := range changes {
				if existing, ok := mergedChanges[key]; ok {
					val.Fields = maps.Clone(val.Fields)
					if val.Old != existing.Old {
						delete(val.Fields, "old_label")
					}
					if label, present := existing.Fields["old_label"]; present {
						if val.Fields == nil {
							val.Fields = make(map[string]json.RawMessage)
						}
						val.Fields["old_label"] = label
					}
					val.Old = existing.Old
				}
				if val.Old == val.New {
					val.Fields = nil
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
			class := plugin.Classify(raw)
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
					providerEntries[class.Key] = indexedRaw{idx: i, raw: merged, kind: class.Kind}
				} else {
					providerEntries[class.Key] = indexedRaw{idx: i, raw: raw, kind: class.Kind}
				}
			default:
				keepAll = append(keepAll, indexedRaw{idx: i, raw: raw})
			}
		}
	}

	var entries []indexedRaw

	// Settings is rebuilt at emit time so the persisted payload reflects only
	// effective net changes; intermediate flips that cancel out are dropped.
	if settings.idx >= 0 {
		effective := map[string]map[string]json.RawMessage{}
		for key, val := range mergedChanges {
			if val.Old != val.New {
				fields := maps.Clone(val.Fields)
				if fields == nil {
					fields = make(map[string]json.RawMessage)
				}
				fields["old"], _ = json.Marshal(val.Old)
				fields["new"], _ = json.Marshal(val.New)
				effective[key] = fields
			}
		}
		if len(effective) > 0 {
			var entry map[string]json.RawMessage
			_ = json.Unmarshal(settings.raw, &entry)
			entry[contracts.NotificationFieldChanges], _ = json.Marshal(effective)
			delete(entry, "contextCleared")
			if data, err := json.Marshal(entry); err == nil {
				entries = append(entries, indexedRaw{idx: settings.idx, raw: data})
			}
		}
	}

	for _, slot := range []indexedRaw{contextCleared, planExec, planUpdated, interrupted, stopIgnored, status, apiRetry} {
		if slot.idx >= 0 {
			entries = append(entries, slot)
		}
	}

	for _, rateLimit := range rateLimitByType {
		entries = append(entries, rateLimit)
	}

	for _, providerEntry := range providerEntries {
		entries = append(entries, providerEntry)
	}

	entries = append(entries, keepAll...)

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].idx < entries[j].idx
	})

	result := make([]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		result = append(result, e.raw)
	}

	if len(result) == 0 {
		return []json.RawMessage{}
	}

	return result
}
