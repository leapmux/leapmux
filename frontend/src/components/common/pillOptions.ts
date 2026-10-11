import type { LucideIcon } from 'lucide-solid'

/** One option for a pill group. */
export interface PillOptionSpec<K> {
  /** The unique selection key. */
  key: K
  label: string
  /**
   * Show this icon when the option has insufficient space for its text.
   * The label remains its accessible name and tooltip text.
   * Screen readers and accessible-name queries can still identify the option.
   */
  icon?: LucideIcon
  /** A non-empty reason that makes this option unavailable. */
  disabledReason?: string
}

/** One through four fixed choices. Use a menu for any other list. */
export type PillOptions<K>
  = | readonly [PillOptionSpec<K>]
    | readonly [PillOptionSpec<K>, PillOptionSpec<K>]
    | readonly [PillOptionSpec<K>, PillOptionSpec<K>, PillOptionSpec<K>]
    | readonly [PillOptionSpec<K>, PillOptionSpec<K>, PillOptionSpec<K>, PillOptionSpec<K>]

export const PILL_OPTION_LIMIT = 4

/**
 * Whether this option list fits the pill group, from one through PILL_OPTION_LIMIT entries.
 * Each caller uses the same limit when it selects a menu for a longer list.
 */
export function isPillOptions<K extends string>(options: readonly PillOptionSpec<K>[]): options is PillOptions<K> {
  return options.length > 0 && options.length <= PILL_OPTION_LIMIT
}

/**
 * Replace every repeated label with the item's distinct text.
 * A unique label keeps its original text and position.
 * PillGroup rejects duplicate keys but permits duplicate labels.
 * Duplicate labels can cause a wrong selection and confuse screen readers.
 * They also make an accessible-name query match more than one option.
 * Each caller supplies the detail that distinguishes its options.
 */
export function disambiguateLabels<T>(
  items: readonly T[],
  label: (item: T) => string,
  distinct: (item: T) => string,
): string[] {
  const counts = new Map<string, number>()
  const labels = items.map(label)
  for (const one of labels)
    counts.set(one, (counts.get(one) ?? 0) + 1)
  return items.map((item, index) => ((counts.get(labels[index]!) ?? 0) > 1 ? distinct(item) : labels[index]!))
}
