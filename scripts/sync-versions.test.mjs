// Tests for sync-versions.mjs, run by `bun test` via `task test-scripts`.
//
// The pure helpers are what carry the risk: `applyClaim` rewrites tracked files
// in place, and its no-match branch is the one thing standing between a
// reworded doc line and a version claim that silently stops being enforced.

import { describe, expect, it } from 'bun:test'

import { CLAIMS, applyClaim, lineAt, major, parseVersions } from './sync-versions.mjs'

const GO_CLAIM = {
  file: 'README.md',
  key: 'GOLANG_VERSION',
  pattern: /^- \*\*Go\*\* (\S+) or later$/gm,
  render: v => `- **Go** ${v} or later`,
}

describe('parseVersions', () => {
  it('reads KEY=VALUE lines and skips comments and blanks', () => {
    const versions = parseVersions('# a comment\n\nGOLANG_VERSION=1.26.5\nNODE_VERSION=24.19.0\n')
    expect(versions.get('GOLANG_VERSION')).toBe('1.26.5')
    expect(versions.get('NODE_VERSION')).toBe('24.19.0')
    expect(versions.size).toBe(2)
  })

  it('keeps a value containing an equals sign intact', () => {
    // Only the FIRST `=` separates; an image ref or a flag-bearing value must
    // survive whole rather than being truncated at the second one.
    expect(parseVersions('K=a=b\n').get('K')).toBe('a=b')
  })

  it('keeps a colon-bearing image reference intact', () => {
    expect(parseVersions('ALPINE_IMAGE=alpine:3.24\n').get('ALPINE_IMAGE')).toBe('alpine:3.24')
  })

  it('rejects a line that is not KEY=VALUE', () => {
    expect(() => parseVersions('GOLANG_VERSION\n')).toThrow(/not a KEY=VALUE line/)
  })

  it('rejects a line starting with the separator', () => {
    expect(() => parseVersions('=orphan\n')).toThrow(/not a KEY=VALUE line/)
  })

  it('returns an empty map for an all-comment file', () => {
    expect(parseVersions('# nothing here\n\n').size).toBe(0)
  })
})

describe('major', () => {
  it('takes the first component', () => {
    expect(major('24.19.0')).toBe('24')
  })

  it('passes through a bare major', () => {
    expect(major('24')).toBe('24')
  })
})

describe('lineAt', () => {
  it('reports 1 for an index on the first line', () => {
    expect(lineAt('a\nb\nc', 0)).toBe(1)
  })

  it('counts newlines before the index', () => {
    expect(lineAt('a\nb\nc', 4)).toBe(3)
  })
})

describe('applyClaim', () => {
  it('rewrites a stale claim and reports the drift', () => {
    const content = 'intro\n- **Go** 1.26.1 or later\noutro\n'
    const { content: rewritten, drifts } = applyClaim(GO_CLAIM, content, '1.26.5')

    expect(rewritten).toBe('intro\n- **Go** 1.26.5 or later\noutro\n')
    expect(drifts).toHaveLength(1)
    expect(drifts[0]).toMatchObject({ file: 'README.md', line: 2, key: 'GOLANG_VERSION', found: '1.26.1' })
  })

  it('reports no drift and changes nothing when the claim already agrees', () => {
    const content = '- **Go** 1.26.5 or later\n'
    const { content: rewritten, drifts } = applyClaim(GO_CLAIM, content, '1.26.5')

    expect(rewritten).toBe(content)
    expect(drifts).toHaveLength(0)
  })

  it('rewrites every occurrence, not just the first', () => {
    // Half-rewriting a file that states the same requirement twice would leave
    // it internally inconsistent -- worse than not rewriting it at all.
    const content = '- **Go** 1.26.1 or later\nmiddle\n- **Go** 1.26.3 or later\n'
    const { content: rewritten, drifts } = applyClaim(GO_CLAIM, content, '1.26.5')

    expect(rewritten.match(/1\.26\.5/g) ?? []).toHaveLength(2)
    expect(rewritten).not.toContain('1.26.1')
    expect(rewritten).not.toContain('1.26.3')
    expect(drifts.map(d => d.line)).toEqual([1, 3])
  })

  it('throws when the pattern matches nothing', () => {
    // The whole point of the script: a reworded claim site must fail loudly
    // rather than be silently skipped and left stale forever.
    expect(() => applyClaim(GO_CLAIM, 'the wording changed entirely\n', '1.26.5'))
      .toThrow(/no line matches the GOLANG_VERSION claim/)
  })

  it('leaves surrounding text byte-identical', () => {
    const content = 'a\n- **Go** 1.0.0 or later\nb\n- **Go** 2.0.0 or later\nc\n'
    const { content: rewritten } = applyClaim(GO_CLAIM, content, '9.9.9')

    expect(rewritten.split('\n').filter(l => !l.startsWith('- **Go**')))
      .toEqual(content.split('\n').filter(l => !l.startsWith('- **Go**')))
  })

  it('is idempotent', () => {
    const once = applyClaim(GO_CLAIM, '- **Go** 1.26.1 or later\n', '1.26.5').content
    const twice = applyClaim(GO_CLAIM, once, '1.26.5')

    expect(twice.content).toBe(once)
    expect(twice.drifts).toHaveLength(0)
  })

  it('does not match a similar line that the pattern excludes', () => {
    // The anchors matter: a prose mention must not be rewritten as if it were
    // the prerequisite bullet.
    const content = 'We tested with **Go** 1.26.1 or later elsewhere.\n- **Go** 1.26.1 or later\n'
    const { content: rewritten } = applyClaim(GO_CLAIM, content, '1.26.5')

    expect(rewritten).toContain('We tested with **Go** 1.26.1 or later elsewhere.')
    expect(rewritten).toContain('- **Go** 1.26.5 or later')
  })
})

describe('CLAIMS registry', () => {
  it('renders output that its own pattern matches, so a rewrite converges', () => {
    // A render/pattern mismatch would make every run report drift and rewrite
    // to something it then fails to recognize.
    for (const claim of CLAIMS) {
      const rendered = claim.render('1.2.3')
      const pattern = new RegExp(claim.pattern.source, claim.pattern.flags)
      expect(pattern.test(rendered), `${claim.file} ${claim.key} renders text its pattern rejects: ${rendered}`).toBe(true)
    }
  })

  it('gives every pattern exactly one capture group, so drift can be reported', () => {
    for (const claim of CLAIMS) {
      const pattern = new RegExp(claim.pattern.source, claim.pattern.flags)
      const match = pattern.exec(claim.render('1.2.3'))
      expect(match, `${claim.file} ${claim.key} pattern did not match its own render`).not.toBeNull()
      expect(match.length, `${claim.file} ${claim.key} must capture exactly one group`).toBe(2)
    }
  })

  it('names only keys that versions.env defines', async () => {
    const { readFileSync } = await import('node:fs')
    const { dirname, join, resolve } = await import('node:path')
    const root = resolve(dirname(new URL(import.meta.url).pathname), '..')
    const versions = parseVersions(readFileSync(join(root, 'versions.env'), 'utf-8'))

    for (const claim of CLAIMS) {
      expect(versions.has(claim.key), `versions.env does not define ${claim.key}`).toBe(true)
    }
  })
})
