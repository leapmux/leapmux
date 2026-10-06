import type { FixtureCase, FixtureRun } from './helpers/launcherFixtureProject'
import { existsSync, readdirSync, rmSync } from 'node:fs'
import { isAbsolute, join, resolve } from 'node:path'
import process from 'node:process'
import { isObject } from '../../src/lib/jsonPick'
import { expect, test } from './fixtures'
import { createLauncherFixtureProject, fixtureStringField, readFixtureRecord, readPlaywrightZipEntries, runLauncherFixtureProject } from './helpers/launcherFixtureProject'

function caseAttachments(testCase: FixtureCase): Record<string, unknown>[] {
  expect(testCase.results).toHaveLength(1)
  const result = testCase.results[0]
  if (!isObject(result) || !Array.isArray(result.attachments))
    throw new Error('The fixture browser case has no attachment array.')
  return result.attachments.map((attachment) => {
    if (!isObject(attachment))
      throw new Error('The fixture browser case contains an invalid attachment.')
    return attachment
  })
}

function retainedPath(run: FixtureRun, attachment: Record<string, unknown>): string {
  const path = fixtureStringField(attachment, 'path')
  return isAbsolute(path) ? path : resolve(run.root, 'frontend', path)
}

function requireTransientCleanup(run: FixtureRun): void {
  expect(readdirSync(join(run.root, '.tmp'))).toEqual([])
  const setups = readdirSync(run.records).filter(file => file.startsWith('setup-'))
  const teardowns = readdirSync(run.records).filter(file => file.startsWith('teardown-'))
  expect(setups).toHaveLength(2)
  expect(teardowns).toHaveLength(2)
  for (const file of setups) {
    const state = readFixtureRecord(join(run.records, file))
    expect(existsSync(fixtureStringField(state, 'runDir'))).toBe(false)
    expect(existsSync(fixtureStringField(state, 'binaryPath'))).toBe(false)
  }
}

function traceRecords(entries: ReadonlyMap<string, Uint8Array>, suffix: string): Record<string, unknown>[] {
  const records: Record<string, unknown>[] = []
  for (const [name, content] of entries) {
    if (!name.endsWith(suffix))
      continue
    for (const line of new TextDecoder('utf8', { fatal: true }).decode(content).split('\n').filter(Boolean)) {
      const record: unknown = JSON.parse(line)
      if (!isObject(record))
        throw new Error('The retained browser trace contains a non-object record.')
      records.push(record)
    }
  }
  return records
}

test.describe('isolated browser shard full tool output', () => {
  test('retains the failed browser trace after both private native shards close', async ({ page }, testInfo) => {
    const root = createLauncherFixtureProject({ browserTrace: true })
    const callerState = { nonce: process.env.LEAPMUX_E2E_NONCE, statePath: process.env.E2E_STATE_PATH, outputFileDir: process.env.LEAPMUX_E2E_OUTPUT_FILE_DIR }
    try {
      const run = await runLauncherFixtureProject(root, { workers: 2, failCases: ['beta'], browserTrace: true })
      await testInfo.attach('fixture-browser-shard-report', { body: JSON.stringify(run.report, null, 2), contentType: 'application/json' })
      expect(run.code).not.toBe(0)
      expect(run.parallelRelease).toBe(true)
      expect(run.report.errors).toEqual([])
      expect(run.report.stats).toMatchObject({ expected: 1, unexpected: 1, skipped: 0, flaky: 0 })
      requireTransientCleanup(run)
      const failed = run.cases.find(testCase => testCase.title === 'executes beta')
      const passed = run.cases.find(testCase => testCase.title === 'executes alpha')
      if (!failed || !passed)
        throw new Error('The fixture browser report omits one selected case.')
      expect(failed.status).toBe('unexpected')
      expect(passed.status).toBe('expected')
      expect(failed.results[0]).toMatchObject({ status: 'failed', error: { message: expect.stringContaining('intentional fixture failure') } })
      expect(caseAttachments(passed).filter(attachment => attachment.name === 'trace')).toEqual([])
      const traces = caseAttachments(failed).filter(attachment => attachment.name === 'trace')
      expect(traces).toHaveLength(1)
      const archivePath = retainedPath(run, traces[0]!)
      expect(existsSync(archivePath)).toBe(true)
      await testInfo.attach('failed-shard-fixture-trace', { path: archivePath, contentType: 'application/zip' })
      const entries = await readPlaywrightZipEntries(archivePath)
      expect([...entries.keys()].some(name => name.endsWith('.trace'))).toBe(true)
      expect([...entries.keys()].some(name => name.endsWith('.network'))).toBe(true)
      const trace = traceRecords(entries, '.trace')
      const network = traceRecords(entries, '.network')
      const beta = readFixtureRecord(join(run.records, 'entry-beta.json'))
      const url = fixtureStringField(beta, 'browserUrl')
      const nonce = fixtureStringField(beta, 'nonce')
      expect(trace.some(record => record.type === 'context-options' && record.browserName === 'chromium')).toBe(true)
      expect(trace.some(record => record.type === 'before' && isObject(record.params) && record.params.url === url)).toBe(true)
      expect(trace.some(record => record.type === 'frame-snapshot' && JSON.stringify(record).includes(nonce))).toBe(true)
      expect(network.some(record => record.type === 'resource-snapshot' && isObject(record.snapshot) && isObject(record.snapshot.request) && record.snapshot.request.url === url && isObject(record.snapshot.response) && record.snapshot.response.status === 200)).toBe(true)
      expect({ nonce: process.env.LEAPMUX_E2E_NONCE, statePath: process.env.E2E_STATE_PATH, outputFileDir: process.env.LEAPMUX_E2E_OUTPUT_FILE_DIR }).toEqual(callerState)
      expect(page.isClosed()).toBe(false)
    }
    finally {
      rmSync(root, { recursive: true, force: true })
    }
  })

  test('removes failure traces from a complete passing browser run', async ({ page }) => {
    const root = createLauncherFixtureProject({ browserTrace: true })
    try {
      const run = await runLauncherFixtureProject(root, { workers: 2, browserTrace: true })
      expect(run.code).toBe(0)
      expect(run.parallelRelease).toBe(true)
      expect(run.report.stats).toMatchObject({ expected: 2, unexpected: 0, skipped: 0, flaky: 0 })
      expect(run.cases.map(testCase => testCase.status)).toEqual(['expected', 'expected'])
      requireTransientCleanup(run)
      for (const testCase of run.cases)
        expect(caseAttachments(testCase).filter(attachment => attachment.name === 'trace')).toEqual([])
      expect(page.isClosed()).toBe(false)
    }
    finally {
      rmSync(root, { recursive: true, force: true })
    }
  })
})
