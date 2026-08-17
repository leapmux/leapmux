import type { SearchEntry } from './search'
import { describe, expect, it } from 'vitest'
import { NAV_GROUPS } from './navGroups'
import { breadcrumb, buildSearchIndex, matchSettings } from './search'

/** The real navigation order, ids not categories — what the dialog passes. */
const ORDER = NAV_GROUPS.map(g => g.id)

function entry(overrides: Partial<SearchEntry> = {}): SearchEntry {
  return {
    groupTitle: 'Appearance',
    navId: 'appearance',
    label: 'Label',
    ...overrides,
  }
}

function match(entries: SearchEntry[], query: string, order: readonly string[] = ORDER) {
  return matchSettings(buildSearchIndex(entries), query, order)
}

describe('matchSettings', () => {
  it('matches case-insensitively over the label', () => {
    const hits = match([entry({ label: 'Turn-end volume' })], 'VOLUME')
    expect(hits).toHaveLength(1)
    expect(hits[0].entries[0].label).toBe('Turn-end volume')
  })

  it('matches help, group title, keywords, and enum option labels', () => {
    const entries = [
      entry({ label: 'By help', navId: 'notifications', groupTitle: 'Notifications', help: 'playback VOLUME' }),
      entry({ label: 'By nothing', navId: 'chat', groupTitle: 'Chat & Composer' }),
      entry({ label: 'By keyword', keywords: ['volume'] }),
      entry({ label: 'By option', optionLabels: ['Dark', 'Light'] }),
    ]
    const hits = match(entries, 'volume')
    expect(hits.flatMap(g => g.entries.map(e => e.label))).toEqual(['By keyword', 'By help'])

    expect(match([entry({ optionLabels: ['Dark'] })], 'dark')).toHaveLength(1)
    expect(match([entry({ groupTitle: 'Chat & Composer', label: 'Nope' })], 'composer')).toHaveLength(1)
  })

  it('groups hits by nav group in navigation order and omits empty groups', () => {
    const entries = [
      entry({ label: 'Advanced label', navId: 'advanced', groupTitle: 'Advanced' }),
      entry({ label: 'Appearance label', navId: 'appearance', groupTitle: 'Appearance' }),
      entry({ label: 'Another', navId: 'appearance', groupTitle: 'Appearance', keywords: ['label'] }),
    ]
    const hits = match(entries, 'label')
    expect(hits.map(g => g.navId)).toEqual(['appearance', 'advanced'])
    expect(hits[0].entries.map(e => e.label)).toEqual(['Appearance label', 'Another'])
  })

  // The user group `advanced` and the admin group `admin-advanced` share the
  // category `advanced`. Grouping by category pushed that one bucket once per
  // group that claimed it, so an admin saw the Advanced results twice.
  it('renders one group per nav id when two groups share a category', () => {
    const shared = NAV_GROUPS.filter(g => g.category === 'advanced')
    expect(shared.map(g => g.id)).toEqual(['advanced', 'admin-advanced'])

    const hits = match([
      entry({ navId: 'advanced', groupTitle: 'Advanced', label: 'Debug logging' }),
      entry({ navId: 'admin-advanced', groupTitle: 'Advanced', label: 'Debug queue' }),
    ], 'debug')
    expect(hits.map(g => g.navId)).toEqual(['advanced', 'admin-advanced'])
    expect(hits.map(g => g.entries.map(e => e.label))).toEqual([['Debug logging'], ['Debug queue']])
  })

  it('omits a group whose nav id the caller did not list', () => {
    const hits = match(
      [entry({ navId: 'admin-advanced', groupTitle: 'Advanced' })],
      'label',
      NAV_GROUPS.filter(g => !g.admin).map(g => g.id),
    )
    expect(hits).toEqual([])
  })

  it('matches nothing on an empty or whitespace query', () => {
    expect(match([entry()], '')).toEqual([])
    expect(match([entry()], '   ')).toEqual([])
  })

  it('renders breadcrumbs as Group › Label', () => {
    expect(breadcrumb(entry({ groupTitle: 'Notifications', label: 'Turn-end sound' })))
      .toBe('Notifications › Turn-end sound')
  })
})

describe('buildSearchIndex', () => {
  it('folds every searchable field into one lower-case haystack', () => {
    const [indexed] = buildSearchIndex([entry({
      label: 'Turn-End Volume',
      help: 'Playback VOLUME',
      groupTitle: 'Notifications',
      keywords: ['Sound'],
      optionLabels: ['Ding Dong'],
    })])
    expect(indexed.haystack).toBe('turn-end volume playback volume notifications sound ding dong')
    expect(indexed.entry.label).toBe('Turn-End Volume')
  })

  it('tolerates an entry with no help, keywords, or option labels', () => {
    const [indexed] = buildSearchIndex([entry({ label: 'Theme' })])
    expect(indexed.haystack).toBe('theme  appearance')
  })
})
