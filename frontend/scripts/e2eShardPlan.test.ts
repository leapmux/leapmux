import type { E2ETestCoverage } from './e2eReports'
import type { FileEstimate } from './e2eShardPlan'
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { assignLongestFirst, estimateFileDurations, formatShardPlan, isTestListPath, mergeDurationHistory, parseDurationHistory, planShards, readDurationHistory, testListContent, writeDurationHistory } from './e2eShardPlan'

const roots: string[] = []
afterEach(() => {
  for (const root of roots.splice(0))
    rmSync(root, { recursive: true, force: true })
})

function destination(name = 'history.json'): string {
  const scratch = resolve(import.meta.dirname, '../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const root = mkdtempSync(join(scratch, 'e2e-shard-plan-test-'))
  roots.push(root)
  return join(root, name)
}

/** Build the selected coverage of the given files, with `casesPerFile` Playwright cases in each. */
function coverage(files: readonly string[], casesPerFile = 1): E2ETestCoverage {
  return {
    files: [...files].sort(),
    cases: files.flatMap(file => Array.from({ length: casesPerFile }, (_, repeatIndex) => ({
      file,
      titlePath: [file, `case ${repeatIndex}`],
      line: 1,
      column: 1,
      projectId: 'mock-chromium',
      projectName: 'mock-chromium',
      repeatIndex,
    }))),
  }
}

function estimate(file: string, estimateMs: number): FileEstimate {
  return { file, estimateMs, source: 'history' }
}

describe('parseDurationHistory', () => {
  it('accepts a complete version 1 history', () => {
    expect(parseDurationHistory({ version: 1, files: { 'a.spec.ts': { durationMs: 100, cases: 2 } } }))
      .toEqual(new Map([['a.spec.ts', { durationMs: 100, cases: 2 }]]))
  })

  const notHistory = 'the file is not version 1 of the duration history'
  const invalidEntry = 'an entry has no valid durationMs and cases'
  it.each([
    { label: 'a non-object', value: [], message: notHistory },
    { label: 'another version', value: { version: 2, files: {} }, message: notHistory },
    { label: 'no file map', value: { version: 1 }, message: notHistory },
    { label: 'an empty file path', value: { version: 1, files: { '': { durationMs: 1, cases: 1 } } }, message: 'an entry has an empty file path' },
    { label: 'a negative duration', value: { version: 1, files: { 'a.spec.ts': { durationMs: -1, cases: 1 } } }, message: invalidEntry },
    { label: 'an infinite duration', value: { version: 1, files: { 'a.spec.ts': { durationMs: Number.POSITIVE_INFINITY, cases: 1 } } }, message: invalidEntry },
    { label: 'a string duration', value: { version: 1, files: { 'a.spec.ts': { durationMs: '1', cases: 1 } } }, message: invalidEntry },
    { label: 'zero cases', value: { version: 1, files: { 'a.spec.ts': { durationMs: 1, cases: 0 } } }, message: invalidEntry },
    { label: 'a fractional case count', value: { version: 1, files: { 'a.spec.ts': { durationMs: 1, cases: 1.5 } } }, message: invalidEntry },
    { label: 'a missing entry field', value: { version: 1, files: { 'a.spec.ts': { durationMs: 1 } } }, message: invalidEntry },
  ])('rejects $label instead of planning from a partial history', ({ value, message }) => {
    expect(() => parseDurationHistory(value)).toThrow(message)
  })
})

describe('readDurationHistory', () => {
  it('reports an absent file without reading it as a failure', () => {
    expect(readDurationHistory(destination())).toEqual({ status: 'absent' })
  })

  it('reads a complete history', () => {
    const path = destination()
    writeFileSync(path, JSON.stringify({ version: 1, files: { 'a.spec.ts': { durationMs: 10, cases: 1 } } }))

    expect(readDurationHistory(path)).toEqual({ status: 'present', files: new Map([['a.spec.ts', { durationMs: 10, cases: 1 }]]) })
  })

  it.each([
    { label: 'invalid JSON', content: '{"version": 1,', reason: 'JSON' },
    { label: 'a malformed history', content: '{"version": 2, "files": {}}', reason: 'version 1' },
  ])('reports $label as an unreadable plan input', ({ content, reason }) => {
    const path = destination()
    writeFileSync(path, content)
    const value = readDurationHistory(path)

    expect(value.status).toBe('unreadable')
    if (value.status !== 'unreadable')
      throw new Error('The history read did not report its failure.')
    expect(value.reason).toContain(reason)
  })

  it('reports a read error other than an absent file as unreadable', () => {
    const path = destination()
    mkdirSync(path, { recursive: true })

    expect(readDurationHistory(path)).toMatchObject({ status: 'unreadable' })
  })
})

describe('mergeDurationHistory', () => {
  it('replaces the entry of each measured file and keeps every other entry', () => {
    const previous = {
      status: 'present' as const,
      files: new Map([
        ['kept.spec.ts', { durationMs: 5, cases: 1 }],
        ['measured.spec.ts', { durationMs: 10, cases: 1 }],
      ]),
    }

    expect(mergeDurationHistory(previous, new Map([['measured.spec.ts', { durationMs: 20, cases: 2 }]])))
      .toEqual(new Map([
        ['kept.spec.ts', { durationMs: 5, cases: 1 }],
        ['measured.spec.ts', { durationMs: 20, cases: 2 }],
      ]))
  })

  it.each([
    { label: 'absent', previous: { status: 'absent' as const } },
    { label: 'unreadable', previous: { status: 'unreadable' as const, reason: 'invalid JSON' } },
  ])('starts a fresh history when the preceding one is $label', ({ previous }) => {
    expect(mergeDurationHistory(previous, new Map([['a.spec.ts', { durationMs: 1, cases: 1 }]])))
      .toEqual(new Map([['a.spec.ts', { durationMs: 1, cases: 1 }]]))
  })
})

describe('writeDurationHistory', () => {
  it('writes the version 1 history with sorted keys, so equal histories produce equal files', () => {
    const first = destination('first.json')
    const second = destination('second.json')
    const entries: [string, { durationMs: number, cases: number }][] = [
      ['b.spec.ts', { durationMs: 2, cases: 1 }],
      ['a.spec.ts', { durationMs: 1, cases: 3 }],
    ]

    writeDurationHistory(first, new Map(entries))
    writeDurationHistory(second, new Map([...entries].reverse()))

    expect(readFileSync(first, 'utf8')).toBe(`${JSON.stringify({ version: 1, files: { 'a.spec.ts': { durationMs: 1, cases: 3 }, 'b.spec.ts': { durationMs: 2, cases: 1 } } }, null, 2)}\n`)
    expect(readFileSync(second, 'utf8')).toBe(readFileSync(first, 'utf8'))
    expect(readdirSync(dirname(first)).filter(file => file.includes('writing'))).toEqual([])
  })
})

describe('estimateFileDurations', () => {
  it('scales the recorded mean case duration to the selected case count', () => {
    const value = estimateFileDurations(coverage(['a.spec.ts', 'b.spec.ts'], 3), new Map([
      ['a.spec.ts', { durationMs: 100, cases: 4 }],
      ['b.spec.ts', { durationMs: 50, cases: 2 }],
    ]))

    expect(value).toEqual([
      { file: 'a.spec.ts', estimateMs: 75, source: 'history' },
      { file: 'b.spec.ts', estimateMs: 75, source: 'history' },
    ])
  })

  it.each([
    { label: 'an even history count', files: ['a.spec.ts', 'b.spec.ts', 'c.spec.ts', 'unknown.spec.ts'], durations: [[100, 1], [200, 1], [400, 1]], median: 200 },
    { label: 'an odd history count', files: ['a.spec.ts', 'b.spec.ts', 'unknown.spec.ts'], durations: [[100, 1], [300, 1]], median: 200 },
  ])('gives a file without history the median estimate of $label', ({ files, durations, median }) => {
    const history = new Map<string, { durationMs: number, cases: number }>(
      durations.map(([durationMs, cases], index) => [files[index]!, { durationMs: durationMs as number, cases: cases as number }]),
    )
    const value = estimateFileDurations(coverage(files), history)
    const unknown = value?.find(entry => entry.file === 'unknown.spec.ts')

    expect(unknown).toEqual({ file: 'unknown.spec.ts', estimateMs: median, source: 'median' })
  })

  it('returns undefined when no selected file has history', () => {
    expect(estimateFileDurations(coverage(['a.spec.ts']), new Map([['other.spec.ts', { durationMs: 1, cases: 1 }]]))).toBeUndefined()
    expect(estimateFileDurations(coverage(['a.spec.ts']), new Map())).toBeUndefined()
  })
})

describe('assignLongestFirst', () => {
  it('assigns the longest file to the shard with the smallest total', () => {
    const shards = assignLongestFirst([
      estimate('a.spec.ts', 100),
      estimate('b.spec.ts', 80),
      estimate('c.spec.ts', 60),
      estimate('d.spec.ts', 40),
    ], 2)

    expect(shards.map(shard => shard.files.map(file => file.file))).toEqual([['a.spec.ts', 'd.spec.ts'], ['b.spec.ts', 'c.spec.ts']])
    expect(shards.map(shard => shard.estimateMs)).toEqual([140, 140])
  })

  it('breaks an estimate tie by path order and a shard tie by the lower index', () => {
    const shards = assignLongestFirst([
      estimate('b.spec.ts', 10),
      estimate('a.spec.ts', 10),
    ], 2)

    expect(shards.map(shard => shard.files.map(file => file.file))).toEqual([['a.spec.ts'], ['b.spec.ts']])
  })

  it('gives every shard a file even when every estimate is zero', () => {
    const shards = assignLongestFirst([
      estimate('a.spec.ts', 0),
      estimate('b.spec.ts', 0),
      estimate('c.spec.ts', 0),
    ], 3)

    expect(shards.map(shard => shard.files.map(file => file.file))).toEqual([['a.spec.ts'], ['b.spec.ts'], ['c.spec.ts']])
  })

  it('limits the shard count to the file count and sorts each shard by path', () => {
    const shards = assignLongestFirst([
      estimate('b.spec.ts', 1),
      estimate('a.spec.ts', 2),
    ], 10)

    expect(shards).toHaveLength(2)
    expect(shards.map(shard => shard.files.map(file => file.file))).toEqual([['a.spec.ts'], ['b.spec.ts']])
  })

  it.each([0, -1, 1.5, Number.NaN])('rejects an invalid shard count: %j', (count) => {
    expect(() => assignLongestFirst([estimate('a.spec.ts', 1)], count)).toThrow('shard count')
  })
})

describe('isTestListPath', () => {
  it.each(['alpha.spec.ts', 'dir with spaces/beta.spec.ts', '01.spec.ts'])('accepts an exact relative file path: %j', (file) => {
    expect(isTestListPath(file)).toBe(true)
  })

  it.each([
    { label: 'an empty path', file: '' },
    { label: 'padding', file: ' alpha.spec.ts' },
    { label: 'a comment', file: '#alpha.spec.ts' },
    { label: 'a project prefix', file: '[mock-chromium] › alpha.spec.ts' },
    { label: 'a line location', file: 'alpha.spec.ts:12' },
    { label: 'a line and column location', file: 'alpha.spec.ts:12:3' },
    { label: 'a title separator', file: 'alpha.spec.ts > suite' },
    { label: 'a newline', file: 'alpha.spec.ts\nbeta.spec.ts' },
    { label: 'a NUL character', file: 'alpha.spec.ts\0beta' },
  ])('rejects $label', ({ file }) => {
    expect(isTestListPath(file)).toBe(false)
  })
})

describe('testListContent', () => {
  it('writes one exact relative path per line with a trailing newline', () => {
    expect(testListContent(['a.spec.ts', 'dir/b.spec.ts'])).toBe('a.spec.ts\ndir/b.spec.ts\n')
  })

  it('rejects an empty selection', () => {
    expect(() => testListContent([])).toThrow('at least one file')
  })

  it('rejects a file that a Playwright test list cannot select exactly', () => {
    expect(() => testListContent(['a.spec.ts:12'])).toThrow('cannot select the file "a.spec.ts:12" exactly')
  })
})

describe('planShards', () => {
  const history = { status: 'present' as const, files: new Map([['a.spec.ts', { durationMs: 10, cases: 1 }], ['b.spec.ts', { durationMs: 30, cases: 1 }]]) }

  it('balances the shards from the duration history', () => {
    const plan = planShards({ coverage: coverage(['a.spec.ts', 'b.spec.ts']), workers: 2, balance: 'history', history, callerTestList: false })

    expect(plan).toEqual({
      kind: 'balanced',
      shards: [
        { files: [{ file: 'b.spec.ts', estimateMs: 30, source: 'history' }], estimateMs: 30 },
        { files: [{ file: 'a.spec.ts', estimateMs: 10, source: 'history' }], estimateMs: 10 },
      ],
    })
  })

  it('limits the shard count to the selected file count', () => {
    const plan = planShards({ coverage: coverage(['a.spec.ts']), workers: 4, balance: 'off', history: { status: 'absent' }, callerTestList: false })

    expect(plan).toEqual({ kind: 'static', total: 1, reason: '--balance=off selects it' })
  })

  it.each([
    { label: 'the caller disables balancing', input: { balance: 'off' as const }, reason: '--balance=off' },
    { label: 'the caller gives a test list', input: { callerTestList: true }, reason: 'one test list only' },
    { label: 'no history exists', input: { history: { status: 'absent' as const } }, reason: 'no duration history exists' },
    { label: 'the history is unreadable', input: { history: { status: 'unreadable' as const, reason: 'invalid JSON' } }, reason: 'the duration history is unreadable: invalid JSON' },
    { label: 'the history holds no selected file', input: { history: { status: 'present' as const, files: new Map([['other.spec.ts', { durationMs: 1, cases: 1 }]]) } }, reason: 'no selected file' },
  ])('keeps Playwright\'s own split when $label', ({ input, reason }) => {
    const plan = planShards({ coverage: coverage(['a.spec.ts', 'b.spec.ts']), workers: 2, balance: 'history', history, callerTestList: false, ...input })

    expect(plan).toEqual({ kind: 'static', total: 2, reason: expect.stringContaining(reason) })
  })

  it('keeps Playwright\'s own split when a test list cannot select a selected file exactly', () => {
    const plan = planShards({ coverage: coverage(['a.spec.ts:12', 'b.spec.ts']), workers: 2, balance: 'history', history, callerTestList: false })

    expect(plan).toEqual({ kind: 'static', total: 2, reason: expect.stringContaining('cannot select the file') })
  })

  it.each([
    { label: 'an empty selection', input: { coverage: coverage([]) }, message: 'at least one selected file' },
    { label: 'a zero worker count', input: { workers: 0 }, message: 'shard count' },
    { label: 'a fractional worker count', input: { workers: 1.5 }, message: 'shard count' },
  ])('rejects $label', ({ input, message }) => {
    expect(() => planShards({ coverage: coverage(['a.spec.ts']), workers: 2, balance: 'history', history, callerTestList: false, ...input })).toThrow(message)
  })
})

describe('formatShardPlan', () => {
  const historyPath = resolve('test-results', '.file-durations.json')

  it('states the reason and the history path of a static plan', () => {
    expect(formatShardPlan({ kind: 'static', total: 3, reason: 'no duration history exists' }, historyPath))
      .toBe(`E2E shard plan: Playwright's own --shard=i/3 split. Reason: no duration history exists. Duration history: ${historyPath}\n`)
  })

  it('lists each balanced shard with its files and their estimates', () => {
    const text = formatShardPlan({
      kind: 'balanced',
      shards: [
        { files: [{ file: 'a.spec.ts', estimateMs: 10_000, source: 'history' }, { file: 'new.spec.ts', estimateMs: 500, source: 'median' }], estimateMs: 10_500 },
        { files: [{ file: 'b.spec.ts', estimateMs: 2000, source: 'history' }], estimateMs: 2000 },
      ],
    }, historyPath)

    expect(text).toBe([
      `E2E shard plan: 2 shards, balanced by the duration history at ${historyPath}`,
      '  Shard 1/2: 2 files, estimated 10.5 s',
      '    a.spec.ts: 10.0 s',
      '    new.spec.ts: 0.5 s (median estimate, no history)',
      '  Shard 2/2: 1 file, estimated 2.0 s',
      '    b.spec.ts: 2.0 s',
      '',
    ].join('\n'))
  })
})
