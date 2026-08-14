import type { FileSortFields, FileSortOrder } from './fileSort'
import { describe, expect, it } from 'vitest'
import {
  compareNames,
  DEFAULT_FILE_SORT_ORDER,
  fileExtension,
  makeFileComparator,
  parseFileSortOrder,
  sortDirectionLabel,
  sortKeyLabel,
} from './fileSort'

interface Entry {
  name: string
  isDir?: boolean
  size?: number
  modTime?: string
}

const FIELDS: FileSortFields<Entry> = {
  name: e => e.name,
  isDir: e => e.isDir ?? false,
  size: e => e.size,
  modTime: e => e.modTime,
}

function order(key: FileSortOrder['key'], direction: FileSortOrder['direction'] = 'asc'): FileSortOrder {
  return { key, direction }
}

function sorted(entries: Entry[], o: FileSortOrder): string[] {
  return entries.toSorted(makeFileComparator(o, FIELDS)).map(e => e.name)
}

describe('compareNames', () => {
  it('ignores case', () => {
    expect(compareNames('apple', 'Banana')).toBeLessThan(0)
    expect(compareNames('Cherry', 'banana')).toBeGreaterThan(0)
  })

  it('breaks a case-only tie deterministically', () => {
    // Both directions must agree, or the two orderings of the same pair would
    // disagree and rows would jump between renders.
    expect(compareNames('README', 'readme')).toBeLessThan(0)
    expect(compareNames('readme', 'README')).toBeGreaterThan(0)
  })

  it('reports identical strings as equal', () => {
    expect(compareNames('same', 'same')).toBe(0)
  })
})

describe('fileExtension', () => {
  it('lowercases the part after the last dot', () => {
    expect(fileExtension('App.TSX')).toBe('tsx')
    expect(fileExtension('archive.tar.gz')).toBe('gz')
  })

  it('treats a dotfile as having no extension', () => {
    expect(fileExtension('.gitignore')).toBe('')
    expect(fileExtension('.env')).toBe('')
  })

  /**
   * The LEADING dot starts a dotfile; a later one still begins an extension.
   * Treating the whole name as extensionless scattered every dotted dotfile
   * into the extensionless group -- `.eslintrc.json` sorted beside `Makefile`
   * instead of beside `tsconfig.json`.
   */
  it('reads the extension of a dotfile that has one', () => {
    expect(fileExtension('.eslintrc.json')).toBe('json')
    expect(fileExtension('.prettierrc.yaml')).toBe('yaml')
    expect(fileExtension('.env.local')).toBe('local')
    expect(fileExtension('.babelrc.js')).toBe('js')
    // Still true through a directory prefix.
    expect(fileExtension('cfg/.eslintrc.json')).toBe('json')
    expect(fileExtension('cfg\\.eslintrc.json')).toBe('json')
  })

  it('groups a dotted dotfile with its own extension under a type sort', () => {
    const fields = {
      name: (e: { name: string }) => e.name,
      isDir: () => false,
      size: () => undefined,
      modTime: () => undefined,
    }
    const entries = [
      { name: 'Makefile' },
      { name: 'tsconfig.json' },
      { name: '.eslintrc.json' },
    ]
    expect(entries.toSorted(makeFileComparator({ key: 'type', direction: 'asc' }, fields)).map(e => e.name))
      // Extensionless first, then the two JSON files together.
      .toEqual(['Makefile', '.eslintrc.json', 'tsconfig.json'])
  })

  it('returns empty for a name with no dot', () => {
    expect(fileExtension('Makefile')).toBe('')
  })

  it('ignores dots in parent directories', () => {
    expect(fileExtension('src/v1.2/Makefile')).toBe('')
    expect(fileExtension('src/v1.2/app.ts')).toBe('ts')
  })
})

/**
 * The collator's locale is PINNED, not the host default. `Intl.Collator(undefined, …)`
 * resolves against whatever locale the browser runs in, and the locales
 * disagree: `sv-SE` sorts `ä` after `z` where `en` sorts it with `a`. Two people
 * would see the same directory in two different orders.
 */
describe('compareNames locale independence', () => {
  it('orders an accented name the same way regardless of the host locale', () => {
    // What a Swedish-locale collator would answer, for contrast.
    expect(new Intl.Collator('sv-SE', { sensitivity: 'accent' }).compare('ä.txt', 'z.txt')).toBeGreaterThan(0)
    // The comparator must not follow the host: `ä` groups with `a`, before `z`.
    expect(compareNames('ä.txt', 'z.txt')).toBeLessThan(0)
  })

  it('still separates an accent from its base letter', () => {
    // `sensitivity: 'accent'` keeps these distinct, so they never fall to the
    // code-unit tiebreak as equal.
    expect(compareNames('resume', 'résumé')).not.toBe(0)
  })
})

describe('parseFileSortOrder', () => {
  it('accepts a well-formed value', () => {
    expect(parseFileSortOrder({ key: 'size', direction: 'desc' })).toEqual({ key: 'size', direction: 'desc' })
  })

  it.each([
    ['null', null],
    ['a string', 'size'],
    ['an unknown key', { key: 'colour', direction: 'asc' }],
    ['an unknown direction', { key: 'size', direction: 'sideways' }],
    ['a missing direction', { key: 'size' }],
  ])('falls back to the default for %s', (_label, raw) => {
    expect(parseFileSortOrder(raw)).toEqual(DEFAULT_FILE_SORT_ORDER)
  })
})

describe('sort labels', () => {
  it('names each criterion', () => {
    expect(sortKeyLabel('modified')).toBe('Last modified')
  })

  it('adapts the direction label to the criterion', () => {
    expect(sortDirectionLabel('name', 'asc')).toBe('A → Z')
    expect(sortDirectionLabel('modified', 'desc')).toBe('Newest first')
    expect(sortDirectionLabel('size', 'asc')).toBe('Smallest first')
  })
})

describe('makeFileComparator', () => {
  it('puts directories first under every criterion and direction', () => {
    const entries: Entry[] = [
      { name: 'zebra.txt', size: 10, modTime: '2026-01-01T00:00:00Z' },
      { name: 'alpha', isDir: true },
      { name: 'yak.txt', size: 5, modTime: '2026-02-01T00:00:00Z' },
      { name: 'zulu', isDir: true },
    ]
    for (const key of ['name', 'modified', 'size', 'type'] as const) {
      for (const direction of ['asc', 'desc'] as const) {
        const names = sorted(entries, order(key, direction))
        expect(names.slice(0, 2).toSorted(), `${key}/${direction}`).toEqual(['alpha', 'zulu'])
      }
    }
  })

  it('keeps directories in path order when the criterion is size', () => {
    const entries: Entry[] = [
      { name: 'zulu', isDir: true },
      { name: 'alpha', isDir: true },
      { name: 'Mid', isDir: true },
    ]
    expect(sorted(entries, order('size', 'asc'))).toEqual(['alpha', 'Mid', 'zulu'])
    expect(sorted(entries, order('size', 'desc'))).toEqual(['alpha', 'Mid', 'zulu'])
  })

  it('keeps directories in path order when the criterion is type or modified', () => {
    const entries: Entry[] = [
      { name: 'zulu', isDir: true, modTime: '2020-01-01T00:00:00Z' },
      { name: 'alpha', isDir: true, modTime: '2026-01-01T00:00:00Z' },
    ]
    expect(sorted(entries, order('type', 'desc'))).toEqual(['alpha', 'zulu'])
    expect(sorted(entries, order('modified', 'desc'))).toEqual(['alpha', 'zulu'])
  })

  it('applies the direction to directories when the criterion is name', () => {
    const entries: Entry[] = [
      { name: 'alpha', isDir: true },
      { name: 'zulu', isDir: true },
    ]
    expect(sorted(entries, order('name', 'desc'))).toEqual(['zulu', 'alpha'])
  })

  it('sorts files by name case-insensitively', () => {
    const entries: Entry[] = [
      { name: 'banana.txt' },
      { name: 'Apple.txt' },
      { name: 'cherry.txt' },
    ]
    expect(sorted(entries, order('name', 'asc'))).toEqual(['Apple.txt', 'banana.txt', 'cherry.txt'])
    expect(sorted(entries, order('name', 'desc'))).toEqual(['cherry.txt', 'banana.txt', 'Apple.txt'])
  })

  it('sorts files by size, then by name on a tie', () => {
    const entries: Entry[] = [
      { name: 'b.txt', size: 100 },
      { name: 'c.txt', size: 5 },
      { name: 'a.txt', size: 100 },
    ]
    expect(sorted(entries, order('size', 'asc'))).toEqual(['c.txt', 'a.txt', 'b.txt'])
    // The tie between the two 100-byte files stays name-ascending under desc:
    // the tiebreak is not flipped, so equal-size rows never reshuffle.
    expect(sorted(entries, order('size', 'desc'))).toEqual(['a.txt', 'b.txt', 'c.txt'])
  })

  it('treats a zero-byte file as the smallest known size, not as unknown', () => {
    const entries: Entry[] = [
      { name: 'unknown.txt' },
      { name: 'empty.txt', size: 0 },
      { name: 'full.txt', size: 9 },
    ]
    expect(sorted(entries, order('size', 'asc'))).toEqual(['empty.txt', 'full.txt', 'unknown.txt'])
  })

  it('sorts files by modification time, then by name on a tie', () => {
    const entries: Entry[] = [
      { name: 'b.txt', modTime: '2026-05-01T10:00:00Z' },
      { name: 'old.txt', modTime: '2020-01-01T00:00:00Z' },
      { name: 'a.txt', modTime: '2026-05-01T10:00:00Z' },
    ]
    expect(sorted(entries, order('modified', 'asc'))).toEqual(['old.txt', 'a.txt', 'b.txt'])
    expect(sorted(entries, order('modified', 'desc'))).toEqual(['a.txt', 'b.txt', 'old.txt'])
  })

  it('sorts unknown size and modification time last under BOTH directions', () => {
    const bySize: Entry[] = [
      { name: 'unknown-a.txt' },
      { name: 'big.txt', size: 900 },
      { name: 'unknown-b.txt' },
      { name: 'small.txt', size: 1 },
    ]
    expect(sorted(bySize, order('size', 'asc'))).toEqual(['small.txt', 'big.txt', 'unknown-a.txt', 'unknown-b.txt'])
    expect(sorted(bySize, order('size', 'desc'))).toEqual(['big.txt', 'small.txt', 'unknown-a.txt', 'unknown-b.txt'])

    const byTime: Entry[] = [
      { name: 'blank.txt', modTime: '' },
      { name: 'new.txt', modTime: '2026-05-01T10:00:00Z' },
      { name: 'absent.txt' },
      { name: 'old.txt', modTime: '2020-05-01T10:00:00Z' },
    ]
    expect(sorted(byTime, order('modified', 'asc'))).toEqual(['old.txt', 'new.txt', 'absent.txt', 'blank.txt'])
    expect(sorted(byTime, order('modified', 'desc'))).toEqual(['new.txt', 'old.txt', 'absent.txt', 'blank.txt'])
  })

  it('groups files by extension, with extensionless entries first', () => {
    const entries: Entry[] = [
      { name: 'app.ts' },
      { name: 'Makefile' },
      { name: 'style.css' },
      { name: '.gitignore' },
      { name: 'index.ts' },
    ]
    expect(sorted(entries, order('type', 'asc'))).toEqual([
      '.gitignore',
      'Makefile',
      'style.css',
      'app.ts',
      'index.ts',
    ])
    // Descending flips the extension groups, but the names inside a group stay
    // ascending because the tiebreak is not flipped.
    expect(sorted(entries, order('type', 'desc'))).toEqual([
      'app.ts',
      'index.ts',
      'style.css',
      '.gitignore',
      'Makefile',
    ])
  })

  it('handles an empty list, a single entry, and all-of-one-kind lists', () => {
    expect(sorted([], order('size', 'desc'))).toEqual([])
    expect(sorted([{ name: 'only.txt', size: 3 }], order('size', 'desc'))).toEqual(['only.txt'])
    expect(sorted([{ name: 'b', isDir: true }, { name: 'a', isDir: true }], order('size'))).toEqual(['a', 'b'])
    expect(sorted([{ name: 'b.txt' }, { name: 'a.txt' }], order('name'))).toEqual(['a.txt', 'b.txt'])
  })

  it('is antisymmetric, so the order does not depend on the input order', () => {
    const entries: Entry[] = [
      { name: 'dir', isDir: true },
      { name: 'a.txt', size: 1, modTime: '2026-01-01T00:00:00Z' },
      { name: 'b.txt', size: 1, modTime: '2026-01-01T00:00:00Z' },
      { name: 'c.md', size: 2, modTime: '2020-01-01T00:00:00Z' },
    ]
    for (const key of ['name', 'modified', 'size', 'type'] as const) {
      for (const direction of ['asc', 'desc'] as const) {
        const o = order(key, direction)
        expect(sorted(entries, o), `${key}/${direction}`).toEqual(sorted(entries.toReversed(), o))
      }
    }
  })
})
