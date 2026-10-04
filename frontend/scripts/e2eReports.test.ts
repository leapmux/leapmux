import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { assertMergedTestCoverage, collectShardBlobs, discoveredTestCoverage, mergedJsonDestination, readDiscoveredTestCoverage, shardReporterEnvironment } from './e2eReports'

const roots: string[] = []
afterEach(() => {
  for (const root of roots.splice(0))
    rmSync(root, { recursive: true, force: true })
})

function directory(): string {
  const scratch = resolve(import.meta.dirname, '../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const root = mkdtempSync(join(scratch, 'e2e-report-test-'))
  roots.push(root)
  return root
}

function report(files: string[]) {
  const specifications = new Map<string, ReturnType<typeof nativeSpec>>()
  for (const [index, file] of files.entries()) {
    const spec = nativeSpec('selected case', { projectId: `project-${index}` })
    spec.file = file
    const existing = specifications.get(file)
    if (existing)
      existing.tests.push(...spec.tests)
    else
      specifications.set(file, spec)
  }
  return {
    errors: [],
    suites: [...specifications].map(([file, spec]) => ({ title: file, file, specs: [spec] })),
  }
}

function nativeSpec(title: string, options: { projectId?: string, projectName?: string, omitProjectId?: boolean, repeats?: number, line?: number } = {}) {
  const projectId = options.projectId ?? 'mock-chromium'
  return {
    id: `native-${title}`,
    title,
    file: 'selected.spec.ts',
    line: options.line ?? 1,
    column: 1,
    tests: Array.from({ length: options.repeats ?? 1 }, () => ({
      ...(options.omitProjectId ? {} : { projectId }),
      projectName: options.projectName ?? projectId,
      results: [],
      status: 'skipped',
    })),
  }
}

function nativeReport(specs: ReturnType<typeof nativeSpec>[], parentTitle = 'selected suite') {
  return {
    errors: [],
    suites: [{
      title: 'selected.spec.ts',
      file: 'selected.spec.ts',
      specs: [],
      suites: [{ title: parentTitle, file: 'selected.spec.ts', specs }],
    }],
  }
}

describe('shardReporterEnvironment', () => {
  it('isolates inherited native last-run state inside the owning shard', () => {
    const env = { PLAYWRIGHT_LAST_RUN_OUTPUT_FILE: resolve('shared-last-run.json') }
    const outputFileDir = resolve('shard-two')
    const value = shardReporterEnvironment(env, outputFileDir)

    expect(value.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE).toBe(join(outputFileDir, 'test-results', '.last-run.json'))
    expect(env.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE).toBe(resolve('shared-last-run.json'))
  })

  it('isolates every inherited reporter path while preserving ordinary environment values', () => {
    const env = { PATH: 'original', PLAYWRIGHT_JSON_OUTPUT_NAME: '../shared.json', PLAYWRIGHT_JSON_OUTPUT_FILE: '/shared.json', PLAYWRIGHT_BLOB_OUTPUT_FILE: '/shared.zip', PLAYWRIGHT_HTML_REPORT: '/shared-html', PLAYWRIGHT_JUNIT_OUTPUT_NAME: '../shared.xml' }
    const path = resolve('shard-one')
    const value = shardReporterEnvironment(env, path)
    expect(value.PATH).toBe(env.PATH)
    expect(value.PLAYWRIGHT_JSON_OUTPUT_FILE).toBe(join(path, 'report.json'))
    expect(value.PLAYWRIGHT_BLOB_OUTPUT_DIR).toBe(join(path, 'blob-report'))
    expect(value.PLAYWRIGHT_HTML_OUTPUT_DIR).toBe(join(path, 'html-report'))
    expect(value.PLAYWRIGHT_JUNIT_OUTPUT_FILE).toBe(join(path, 'junit.xml'))
    expect(value.PLAYWRIGHT_JSON_OUTPUT_NAME).toBeUndefined()
    expect(value.PLAYWRIGHT_BLOB_OUTPUT_FILE).toBeUndefined()
    expect(value.PLAYWRIGHT_HTML_REPORT).toBeUndefined()
    expect(value.PLAYWRIGHT_JUNIT_OUTPUT_NAME).toBeUndefined()
    expect(env.PLAYWRIGHT_JSON_OUTPUT_FILE).toBe('/shared.json')
  })
})

describe('mergedJsonDestination', () => {
  it('preserves the native explicit-file precedence', () => {
    expect(mergedJsonDestination({ PLAYWRIGHT_JSON_OUTPUT_FILE: 'explicit.json', PLAYWRIGHT_JSON_OUTPUT_NAME: 'ignored.json' }, resolve('cwd'), resolve('artifacts')))
      .toBe(resolve('cwd', 'explicit.json'))
  })

  it('preserves the native output directory and name combination', () => {
    expect(mergedJsonDestination({ PLAYWRIGHT_JSON_OUTPUT_DIR: '../reports', PLAYWRIGHT_JSON_OUTPUT_NAME: 'native.json' }, resolve('cwd'), resolve('artifacts')))
      .toBe(resolve('cwd', '../reports/native.json'))
  })

  it('keeps the default combined report in its retained full tool output directory', () => {
    const path = resolve('artifacts')
    expect(mergedJsonDestination({}, resolve('cwd'), path)).toBe(join(path, 'report.json'))
  })
})

describe('discoveredTestCoverage files', () => {
  it('keeps exact file filters and consolidates files repeated across projects', () => {
    expect(discoveredTestCoverage(report(['b.spec.ts', 'file with spaces.spec.ts', 'b.spec.ts'])).files)
      .toEqual(['b.spec.ts', 'file with spaces.spec.ts'])
  })

  it('accepts an explicitly empty native discovery result', () => {
    expect(discoveredTestCoverage(report([])).files).toEqual([])
  })

  it('accepts native leaf suites that omit their child suite array', () => {
    expect(discoveredTestCoverage(nativeReport([nativeSpec('selected case')])).files).toEqual(['selected.spec.ts'])
  })

  it.each([null, 'not a suite array', {}])('rejects a malformed present child suite value: %j', (suites) => {
    expect(() => discoveredTestCoverage({
      errors: [],
      suites: [{ title: 'selected.spec.ts', specs: [nativeSpec('selected case')], suites }],
    })).toThrow('Playwright report')
  })

  it.each([null, {}, { suites: [], errors: ['startup failed'] }, { suites: [{}], errors: [] }, { suites: [{ specs: [{ file: 'a', tests: [] }], suites: [] }], errors: [] }])('rejects malformed or failed discovery: %j', (value) => {
    expect(() => discoveredTestCoverage(value)).toThrow('Playwright report')
  })
})

describe('assertMergedTestCoverage', () => {
  it.each([
    {
      label: 'missing case inside one selected file',
      discovery: nativeReport([nativeSpec('first'), nativeSpec('second')]),
      merged: nativeReport([nativeSpec('first')]),
    },
    {
      label: 'additional case inside one selected file',
      discovery: nativeReport([nativeSpec('first')]),
      merged: nativeReport([nativeSpec('first'), nativeSpec('second')]),
    },
    {
      label: 'changed selected project',
      discovery: nativeReport([nativeSpec('first', { projectId: 'one' })]),
      merged: nativeReport([nativeSpec('first', { projectId: 'two' })]),
    },
    {
      label: 'changed enclosing suite',
      discovery: nativeReport([nativeSpec('first')], 'one'),
      merged: nativeReport([nativeSpec('first')], 'two'),
    },
    {
      label: 'missing selected repeat',
      discovery: nativeReport([nativeSpec('first', { repeats: 2 })]),
      merged: nativeReport([nativeSpec('first')]),
    },
    {
      label: 'additional selected repeat',
      discovery: nativeReport([nativeSpec('first')]),
      merged: nativeReport([nativeSpec('first', { repeats: 2 })]),
    },
    {
      label: 'changed source location',
      discovery: nativeReport([nativeSpec('first', { line: 10 })]),
      merged: nativeReport([nativeSpec('first', { line: 20 })]),
    },
  ])('rejects a $label even when the file set matches', ({ discovery, merged }) => {
    const path = join(directory(), 'merged.json')
    writeFileSync(path, JSON.stringify(merged))

    expect(() => assertMergedTestCoverage(path, discoveredTestCoverage(discovery).cases)).toThrow('selected test')
  })

  it('accepts every selected project and repeat despite a different native record order', () => {
    const first = nativeSpec('first', { projectId: 'one', repeats: 2 })
    first.tests.push(...nativeSpec('first', { projectId: 'two', repeats: 3 }).tests)
    const second = nativeSpec('second', { projectId: 'one' })
    const discovery = nativeReport([first, second])
    const mergedFirst = structuredClone(first)
    mergedFirst.tests.reverse()
    const path = join(directory(), 'merged.json')
    writeFileSync(path, JSON.stringify(nativeReport([second, mergedFirst])))

    expect(() => assertMergedTestCoverage(path, discoveredTestCoverage(discovery).cases)).not.toThrow()
  })

  it('accepts an empty native selection without inventing a case', () => {
    const path = join(directory(), 'merged.json')
    writeFileSync(path, JSON.stringify({ errors: [], suites: [] }))

    expect(() => assertMergedTestCoverage(path, [])).not.toThrow()
  })

  it('accepts native merged projects without IDs while preserving each project and repeat', () => {
    const discovery = nativeReport([
      nativeSpec('selected', { projectId: 'one', repeats: 2 }),
      nativeSpec('selected', { projectId: 'two', repeats: 3 }),
    ])
    const merged = nativeReport([
      nativeSpec('selected', { projectId: 'two', repeats: 3, omitProjectId: true }),
      nativeSpec('selected', { projectId: 'one', repeats: 2, omitProjectId: true }),
    ])
    const path = join(directory(), 'merged.json')
    writeFileSync(path, JSON.stringify(merged))

    expect(() => assertMergedTestCoverage(path, discoveredTestCoverage(discovery).cases)).not.toThrow()
  })

  it('rejects ambiguous merged names for different selected project IDs', () => {
    const discovery = nativeReport([
      nativeSpec('selected', { projectId: 'shared', projectName: 'shared' }),
      nativeSpec('selected', { projectId: 'shared1', projectName: 'shared' }),
    ])
    const merged = nativeReport([
      nativeSpec('selected', { projectName: 'shared', repeats: 2, omitProjectId: true }),
    ])
    const path = join(directory(), 'merged.json')
    writeFileSync(path, JSON.stringify(merged))

    expect(() => assertMergedTestCoverage(path, discoveredTestCoverage(discovery).cases)).toThrow('selected test')
  })
})

describe('discoveredTestCoverage', () => {
  it('counts repeats separately for each native specification and project', () => {
    const first = nativeSpec('first', { repeats: 2 })
    first.tests.push(...nativeSpec('first', { projectId: 'second-project', repeats: 2 }).tests)
    const value = discoveredTestCoverage(nativeReport([first, nativeSpec('second')]))

    expect(value.files).toEqual(['selected.spec.ts'])
    expect(value.cases).toEqual([
      { file: 'selected.spec.ts', titlePath: ['selected.spec.ts', 'selected suite', 'first'], line: 1, column: 1, projectId: 'mock-chromium', projectName: 'mock-chromium', repeatIndex: 0 },
      { file: 'selected.spec.ts', titlePath: ['selected.spec.ts', 'selected suite', 'first'], line: 1, column: 1, projectId: 'mock-chromium', projectName: 'mock-chromium', repeatIndex: 1 },
      { file: 'selected.spec.ts', titlePath: ['selected.spec.ts', 'selected suite', 'first'], line: 1, column: 1, projectId: 'second-project', projectName: 'second-project', repeatIndex: 0 },
      { file: 'selected.spec.ts', titlePath: ['selected.spec.ts', 'selected suite', 'first'], line: 1, column: 1, projectId: 'second-project', projectName: 'second-project', repeatIndex: 1 },
      { file: 'selected.spec.ts', titlePath: ['selected.spec.ts', 'selected suite', 'second'], line: 1, column: 1, projectId: 'mock-chromium', projectName: 'mock-chromium', repeatIndex: 0 },
    ])
  })

  it('preserves empty native project names and zero source positions', () => {
    const spec = nativeSpec('', { projectId: '', line: 0 })
    spec.column = 0
    const value = discoveredTestCoverage(nativeReport([spec], ''))

    expect(value.cases[0]).toEqual({ file: 'selected.spec.ts', titlePath: ['selected.spec.ts', '', ''], line: 0, column: 0, projectId: '', projectName: '', repeatIndex: 0 })
  })

  it('uses the native project name only when the merged project ID is absent', () => {
    const value = discoveredTestCoverage(nativeReport([nativeSpec('selected', { projectId: 'one', omitProjectId: true })]))

    expect(value.cases[0]?.projectId).toBe('one')
    expect(value.cases[0]?.projectName).toBe('one')
  })

  it('preserves a present native project ID that differs from its display name', () => {
    const value = discoveredTestCoverage(nativeReport([nativeSpec('selected', { projectId: 'one-native', projectName: 'one-display' })]))

    expect(value.cases[0]?.projectId).toBe('one-native')
    expect(value.cases[0]?.projectName).toBe('one-display')
  })

  it('counts native repeats across separate specification records', () => {
    const value = discoveredTestCoverage(nativeReport([nativeSpec('selected'), nativeSpec('selected')]))

    expect(value.cases.map(test => test.repeatIndex)).toEqual([0, 1])
  })

  it.each([
    { label: 'absent test title', spec: { ...nativeSpec('first'), title: undefined } },
    { label: 'non-string test title', spec: { ...nativeSpec('first'), title: false } },
    { label: 'absent source line', spec: { ...nativeSpec('first'), line: undefined } },
    { label: 'negative source line', spec: { ...nativeSpec('first'), line: -1 } },
    { label: 'noninteger source line', spec: { ...nativeSpec('first'), line: 1.5 } },
    { label: 'infinite source line', spec: { ...nativeSpec('first'), line: Number.POSITIVE_INFINITY } },
    { label: 'unsafe source line', spec: { ...nativeSpec('first'), line: Number.MAX_SAFE_INTEGER + 1 } },
    { label: 'absent source column', spec: { ...nativeSpec('first'), column: undefined } },
    { label: 'negative source column', spec: { ...nativeSpec('first'), column: -1 } },
    { label: 'non-object project record', spec: { ...nativeSpec('first'), tests: [null] } },
    { label: 'non-string project ID', spec: { ...nativeSpec('first'), tests: [{ projectId: false, projectName: 'one' }] } },
    { label: 'null project ID', spec: { ...nativeSpec('first'), tests: [{ projectId: null, projectName: 'one' }] } },
    { label: 'absent project name', spec: { ...nativeSpec('first'), tests: [{ projectId: 'one' }] } },
  ])('rejects invalid identities instead of dropping the native case: $label', ({ spec }) => {
    expect(() => discoveredTestCoverage({ errors: [], suites: [{ title: 'selected.spec.ts', specs: [spec] }] })).toThrow('Playwright report')
  })

  it('rejects a missing suite title instead of changing the case identity', () => {
    expect(() => discoveredTestCoverage({ errors: [], suites: [{ specs: [nativeSpec('first')] }] })).toThrow('suite title')
  })
})

describe('readDiscoveredTestCoverage', () => {
  it('reads the complete native selected case records from disk', () => {
    const path = join(directory(), 'discovery.json')
    const source = nativeReport([nativeSpec('selected', { repeats: 2 })])
    writeFileSync(path, JSON.stringify(source))

    expect(readDiscoveredTestCoverage(path)).toEqual(discoveredTestCoverage(source))
  })

  it('returns an absent report file error', () => {
    expect(() => readDiscoveredTestCoverage(join(directory(), 'absent.json'))).toThrow('ENOENT')
  })

  it('returns a malformed report JSON error', () => {
    const path = join(directory(), 'malformed.json')
    writeFileSync(path, '{')

    expect(() => readDiscoveredTestCoverage(path)).toThrow(SyntaxError)
  })
})

describe('collectShardBlobs', () => {
  it('preserves both shard blobs even when native filenames match', () => {
    const root = directory()
    const shards = [join(root, 'one'), join(root, 'two')]
    for (const [index, shard] of shards.entries()) {
      mkdirSync(join(shard, 'blob-report'), { recursive: true })
      writeFileSync(join(shard, 'blob-report/report.zip'), `shard ${index}`)
    }
    const destination = join(root, 'combined')
    collectShardBlobs(shards, destination)
    expect(readFileSync(join(destination, 'shard-1-report.zip'), 'utf8')).toBe('shard 0')
    expect(readFileSync(join(destination, 'shard-2-report.zip'), 'utf8')).toBe('shard 1')
  })

  it('rejects an absent or empty native report', () => {
    const root = directory()
    mkdirSync(join(root, 'blob-report'))
    expect(() => collectShardBlobs([root], join(root, 'combined'))).toThrow('no unique')
    writeFileSync(join(root, 'blob-report/report.zip'), '')
    expect(() => collectShardBlobs([root], join(root, 'combined'))).toThrow('incomplete')
  })

  it('rejects a report symlink without reading its target', () => {
    const root = directory()
    mkdirSync(join(root, 'blob-report'))
    mkdirSync(join(root, 'target'))
    symlinkSync(join(root, 'target'), join(root, 'blob-report/report.zip'), 'junction')
    expect(() => collectShardBlobs([root], join(root, 'combined'))).toThrow('no unique')
  })
})
