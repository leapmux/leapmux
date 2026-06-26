import type { AgentProvider, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import { getOrCreate } from '~/lib/getOrCreate'

/**
 * Per-provider cache of option display names, populated from AgentInfo's option
 * groups. Used by notification renderers to show human-readable labels for
 * settings changes when the notification payload doesn't include inline labels
 * (e.g. older messages). Model and effort are ordinary groups here.
 *
 * Keyed by PROVIDER, not globally: the same group id carries different option sets
 * across providers (e.g. `permissionMode` is Cursor's Agent/Plan/Ask vs Goose's
 * Auto/Approve/Smart Approve/Chat), so a single global map let one provider's labels
 * collide with another's last-writer-wins. Two agents of the SAME provider share an
 * identical label space, so per-provider keying is both correct and minimal.
 */

/**
 * One cache entry per (provider, group id): the group's display label and its option-id -> name
 * sub-map, co-located in a single record. A single map keyed by group id therefore evicts a
 * group's label and its options TOGETHER, so the LRU bound can never half-evict (drop the option
 * sub-map while keeping the label, or vice versa) -- a hazard a pair of parallel maps with
 * independent eviction populations would carry.
 */
interface GroupLabelEntry {
  label?: string
  options: Map<string, string> // option id -> display name
}

const optionGroupCache = new Map<AgentProvider, Map<string, GroupLabelEntry>>()

// Upper bound on retained option labels per (provider, group). The per-group option-id ->
// name map can grow over a very long session with heavy churn (e.g. many distinct Cursor
// model variants, repeated effort switches), accumulating every id ever seen. 256 is
// generous enough that historical settings_changed rows in a normal session keep their labels.
const MAX_LABELS_PER_GROUP = 256

// Upper bound on retained GROUP ids per provider. Real providers expose a handful of groups
// (model, effort, permission mode, a few config options), so this only bites a non-conforming
// server cycling distinct group ids -- the same adversary the per-group option-id cap defends
// against. Without it, that dimension would grow without bound.
const MAX_GROUPS_PER_PROVIDER = 64

/**
 * Insert or refresh `map[key] = value` with LRU eviction at `max`. A Map preserves insertion
 * order and re-`set`ting an existing key does NOT refresh it, so delete-then-set moves the key
 * to the most-recent position. Each AgentInfo re-sets the current catalog's keys (keeping them
 * fresh), so a key that stops appearing drifts to the front and is evicted first -- exactly the
 * keys least likely to be referenced by a still-visible row. Generic over the value so the
 * group-id dimension (sub-maps) is bounded the same way the option-id dimension (labels) is.
 */
function setWithCap<V>(map: Map<string, V>, key: string, value: V, max: number): void {
  map.delete(key)
  map.set(key, value)
  while (map.size > max) {
    const oldest = map.keys().next().value
    if (oldest === undefined)
      break
    map.delete(oldest)
  }
}

/** Update the cache from an agent's option-group catalog (called when AgentInfo arrives). */
export function updateSettingsLabelCache(provider: AgentProvider, optionGroups?: AvailableOptionGroup[]): void {
  if (!optionGroups)
    return
  const groups = getOrCreate(optionGroupCache, provider, () => new Map<string, GroupLabelEntry>())
  const seenInPush = new Set<string>()
  for (const group of optionGroups) {
    // A catalog carries one group per id (the backend dedups before broadcast). Defend against a
    // malformed catalog with two same-id groups by taking the FIRST and skipping later ones, so the
    // entry reflects a single group rather than silently merging two groups' option sets (and
    // last-writer-wins labels) under one id.
    if (seenInPush.has(group.id))
      continue
    seenInPush.add(group.id)
    // One entry per group id, LRU-bounded at MAX_GROUPS_PER_PROVIDER so a non-conforming server
    // cycling distinct group ids can't grow the map without bound. The entry holds BOTH the
    // group's label and its option sub-map, so the single setWithCap below refreshes -- and, on
    // overflow, evicts -- the two together; neither can half-evict while the other is still live.
    const entry = groups.get(group.id) ?? { options: new Map<string, string>() }
    // Keep the previously-cached label when this push omits it (a label-less push still refreshes
    // the entry's LRU position via setWithCap below).
    if (group.label)
      entry.label = group.label
    // Append-merge into the group's existing option sub-map rather than replacing it
    // wholesale: this cache exists to resolve labels for HISTORICAL settings_changed
    // notifications (re-rendered on scroll-back) whose payload carries no inline label.
    // A model/effort switch narrows the catalog (e.g. dropping "ultracode" after moving
    // off Opus), so a wholesale replace would EVICT the label an older notification still
    // references, leaving it to render the raw id. Retaining recently-seen labels keeps
    // those historical rows readable; a renamed id still picks up its new name here because
    // `set` overwrites the same key. Growth is bounded by setWithCap's LRU eviction,
    // so an evicted (long-unseen) id falls back to its raw value in an old row.
    for (const opt of group.options) {
      if (opt.name)
        setWithCap(entry.options, opt.id, opt.name, MAX_LABELS_PER_GROUP)
    }
    setWithCap(groups, group.id, entry, MAX_GROUPS_PER_PROVIDER)
  }
}

/**
 * Clear every cached label. Lets tests that populate the cache via
 * {@link updateSettingsLabelCache} isolate their state instead of leaking
 * registrations into later cases.
 */
export function clearSettingsLabelCache(): void {
  optionGroupCache.clear()
}

/**
 * Look up a cached display name for an option value within a group (by group id), scoped
 * to the agent's provider. Returns undefined when the provider is unknown (then the caller
 * falls back to the raw id) so a notification without provider context degrades gracefully.
 */
export function getCachedSettingsLabel(provider: AgentProvider | undefined, key: string, id: string): string | undefined {
  if (provider === undefined)
    return undefined
  return optionGroupCache.get(provider)?.get(key)?.options.get(id)
}

export function getCachedSettingsGroupLabel(provider: AgentProvider | undefined, key: string): string | undefined {
  if (provider === undefined)
    return undefined
  return optionGroupCache.get(provider)?.get(key)?.label
}
