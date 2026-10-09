package service

import (
	"context"
	"log/slog"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// optionsCASMaxAttempts limits comparison retries before the final merged write.
const optionsCASMaxAttempts = 8

// optionsCASResult retains only this operation's output and unlocked reports.
type optionsCASResult struct {
	options   string
	wrote     bool
	exhausted bool
	reports   []error
}

func (result optionsCASResult) report(agentID string) {
	for _, err := range result.reports {
		slog.Warn("invalid agent options payload; using empty object", "error", err)
	}
	if result.exhausted {
		slog.Warn("options CAS exhausted; applied final last-writer-wins merge", "agent_id", agentID, "attempts", optionsCASMaxAttempts)
	}
}

func (result optionsCASResult) settled(options string, wrote bool) optionsCASResult {
	result.options, result.wrote = options, wrote
	return result
}

func parseCASOptions(raw string, result *optionsCASResult) OptionMap {
	parsed, err := optionmap.ParseWithError(raw)
	if err != nil {
		result.reports = append(result.reports, err)
	}
	return parsed
}

// withoutStaleClears removes a clear when its comparison snapshot never held that key.
// A genuine clear remains in the delta. Each retry narrows against its own snapshot.
func withoutStaleClears(refresh, snapshot map[string]string) map[string]string {
	hasStale := false
	for key, value := range refresh {
		if value == "" {
			if _, present := snapshot[key]; !present {
				hasStale = true
				break
			}
		}
	}
	if !hasStale {
		return refresh
	}
	filtered := make(map[string]string, len(refresh))
	for key, value := range refresh {
		if value == "" {
			if _, present := snapshot[key]; !present {
				continue
			}
		}
		filtered[key] = value
	}
	return filtered
}

// narrowedOptionDelta retains the existing caller-facing narrowing operation.
func narrowedOptionDelta(expected string, delta map[string]string) (narrowed map[string]string, base, merged string) {
	result := optionsCASResult{}
	narrowed, base, merged = narrowedCASOptionDelta(expected, delta, &result)
	result.report("")
	return narrowed, base, merged
}

// narrowedCASOptionDelta performs the same merge without a logger callback.
func narrowedCASOptionDelta(expected string, delta map[string]string, result *optionsCASResult) (narrowed map[string]string, base, merged string) {
	snapshot := parseCASOptions(expected, result)
	narrowed = withoutStaleClears(delta, snapshot)
	base = marshalOptions(snapshot)
	merged = marshalOptions(mergeOptions(snapshot, narrowed))
	return narrowed, base, merged
}

// casPersistAgentOptions merges one delta through the original query handle.
// It preserves concurrent keys and returns local reports instead of invoking a logger.
func casPersistAgentOptions(ctx context.Context, queries *db.Queries, agentID, expected string, refresh map[string]string) (optionsCASResult, error) {
	result := optionsCASResult{}
	for attempt := 0; attempt < optionsCASMaxAttempts; attempt++ {
		var base, options string
		refresh, base, options = narrowedCASOptionDelta(expected, refresh, &result)
		if options == base {
			// A stale snapshot cannot prove a no-op. Read and merge the actual row also.
			row, err := queries.GetAgentByID(ctx, agentID)
			if err != nil {
				return result, err
			}
			liveOptions := parseCASOptions(row.Options, &result)
			live := marshalOptions(liveOptions)
			if marshalOptions(mergeOptions(liveOptions, refresh)) == live {
				return result.settled(live, false), nil
			}
			expected = row.Options
			continue
		}
		changed, err := queries.SetAgentOptionsIfUnchanged(ctx, db.SetAgentOptionsIfUnchangedParams{
			Options: options, ID: agentID, ExpectedOptions: base,
		})
		if err != nil {
			return result, err
		}
		if changed > 0 {
			return result.settled(options, true), nil
		}
		row, err := queries.GetAgentByID(ctx, agentID)
		if err != nil {
			return result, err
		}
		expected = row.Options
	}
	// The final merge preserves the latest row's unrelated keys after eight competing writes.
	// Its unconditional write retains the original last-writer-wins behavior.
	row, err := queries.GetAgentByID(ctx, agentID)
	if err != nil {
		return result, err
	}
	_, base, options := narrowedCASOptionDelta(row.Options, refresh, &result)
	if options == base {
		return result.settled(base, false), nil
	}
	if err := queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{Options: options, ID: agentID}); err != nil {
		return result, err
	}
	result.exhausted = true
	return result.settled(options, true), nil
}

// casPersistConfirmedSettings keeps options and catalog in one conditional statement per attempt.
// Its catalog comparison preserves a concurrently discovered richer catalog.
func casPersistConfirmedSettings(ctx context.Context, queries *db.Queries, agentID, expectedOptions string, delta map[string]string, expectedCatalog, catalog string) (db.Agent, error) {
	for attempt := 0; attempt < optionsCASMaxAttempts; attempt++ {
		var base, options string
		delta, base, options = narrowedOptionDelta(expectedOptions, delta)
		row, err := queries.UpdateAgentConfirmedSettings(ctx, db.UpdateAgentConfirmedSettingsParams{
			ExpectedOptions: base, Options: options, ExpectedOptionGroups: expectedCatalog,
			OptionGroups: catalog, ID: agentID,
		})
		if err != nil {
			return db.Agent{}, err
		}
		if row.Options == options {
			// An options-only writer can store the identical blob without its catalog.
			// Reassert our catalog only while the original catalog comparison still matches.
			if catalog != "" && row.OptionGroups != catalog && row.OptionGroups == expectedCatalog {
				updated, err := queries.SetAgentOptionGroupsIfUnchanged(ctx, db.SetAgentOptionGroupsIfUnchangedParams{
					OptionGroups: catalog, ExpectedOptionGroups: expectedCatalog, ID: agentID,
				})
				if err != nil {
					return db.Agent{}, err
				}
				if updated > 0 {
					row.OptionGroups = catalog
				}
			}
			return row, nil
		}
		expectedOptions = row.Options
	}
	slog.Warn("confirmed-settings atomic CAS exhausted; applied non-atomic fallback", "agent_id", agentID, "attempts", optionsCASMaxAttempts)
	result, err := casPersistAgentOptions(ctx, queries, agentID, expectedOptions, delta)
	result.report(agentID)
	if err != nil {
		return db.Agent{}, err
	}
	if catalog != "" || expectedCatalog != "" {
		if _, err := queries.SetAgentOptionGroupsIfUnchanged(ctx, db.SetAgentOptionGroupsIfUnchangedParams{
			OptionGroups: catalog, ExpectedOptionGroups: expectedCatalog, ID: agentID,
		}); err != nil {
			return db.Agent{}, err
		}
	}
	return queries.GetAgentByID(ctx, agentID)
}
