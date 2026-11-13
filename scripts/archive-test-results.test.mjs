import { Buffer } from 'node:buffer'
import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, readdirSync, readFileSync, symlinkSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { afterEach, describe, expect, it } from 'bun:test'
import { archiveTestResults, collectTestResultFiles } from './archive-test-results.mjs'
import { scratchDirectories, writeFiles } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)

describe('archiveTestResults', () => {
  for (const scenario of [
    { label: 'ASCII paths', prefix: 'node-archive', file: 'report.log' },
    { label: 'spaces in the working directory', prefix: 'node archive spaces', file: 'report.log' },
    { label: 'a Unicode report filename', prefix: 'node-archive', file: '日本語.log' },
    { label: 'a Unicode working directory', prefix: 'node archive 日本語', file: 'report.log' },
  ]) {
    it(`creates an archive through Node with ${scenario.label}`, () => {
      const root = scratch.create(scenario.prefix)
      writeFiles(root, { [`.tmp/test-results/${scenario.file}`]: 'report contents' })
      const module = pathToFileURL(resolve(import.meta.dirname, 'archive-test-results.mjs')).href
      const program = `import { archiveTestResults } from ${JSON.stringify(module)}; console.log(await archiveTestResults({ root: process.argv[1] }));`
      const result = spawnSync('node', [
        '--input-type=module',
        '-e',
        program,
        root,
      ], { encoding: 'utf8' })
      expect(result.status, result.stderr).toBe(0)
    })
  }

  it('preserves Unicode paths in a streamed archive', async () => {
    const root = scratch.create('archive 日本語 paths')
    const file = '.tmp/test-results/space 日本語.log'
    writeFiles(root, { [file]: 'Unicode report contents\n' })
    const archive = await archiveTestResults({ root })
    const destination = scratch.create('extracted-unicode-reports')
    const extracted = spawnSync('tar', ['-xzf', '-'], {
      cwd: destination,
      input: readFileSync(archive),
      encoding: 'utf8',
    })
    expect(extracted.status, extracted.stderr).toBe(0)
    expect(readFileSync(join(destination, file), 'utf8')).toBe('Unicode report contents\n')
  })

  it.each([0, 1])('writes GitHub output for an input file count of %i', (count) => {
    const root = scratch.create('archive-cli')
    if (count)
      writeFiles(root, { '.tmp/test-results/console.log': 'test output' })
    const output = join(root, 'github-output')
    const result = spawnSync(process.execPath, [
      resolve(import.meta.dirname, 'archive-test-results.mjs'),
    ], {
      cwd: root,
      env: { ...process.env, GITHUB_OUTPUT: output, TEST_RESULTS_ARCHIVE_NAME: 'fixture-results.tar.gz' },
      encoding: 'utf8',
    })
    expect(result.status, result.stderr).toBe(0)
    const [, archive] = readFileSync(output, 'utf8').trimEnd().split('\n')
    expect(archive).toBe(count
      ? join(root, '.tmp/test-results-archives/fixture-results.tar.gz')
      : '')
  })
  it('compresses all report sources into one archive with relative paths and complete bytes', async () => {
    const root = scratch.create('all test reports')
    const files = {
      '.tmp/test-results/backend-private/console.log': 'Go test output\n',
      '.tmp/test-results/desktop-private/result.json': '{"exitCode":0}',
      '.tmp/test-results/frontend-private/vitest.xml': '<testsuites/>',
      '.tmp/e2e-launcher-fixture-failed/retained-artifacts/.last-run.json': '{"status":"passed"}',
      '.tmp/e2e-launcher-fixture-failed/retained-artifacts/combined.json': '{}',
      '.tmp/e2e-launcher-fixture-failed/retained-artifacts/runs/private/shard-1/console.log': 'shard output',
      '.tmp/e2e-launcher-fixture-failed/retained-artifacts/runs/private/shard-1/blob-report/report.zip': 'shard blob',
      '.tmp/e2e-launcher-fixture-failed/retained-artifacts/runs/private/shard-1/test-results/failed/attachments/receipt.txt': 'receipt',
      '.tmp/e2e-launcher-fixture-failed/launcher-artifacts/report.json': '{}',
      '.tmp/e2e-launcher-fixture-failed/launcher-console-private.log': 'cleanup failed\n',
      '.tmp/e2e-launcher-fixture-failed/first-report.json': '{}',
      '.tmp/e2e-launcher-fixture-failed/records/entry-alpha.json': '{}',
      '.tmp/e2e-launcher-fixture-failed/records/exit-alpha.json': '{}',
      'frontend/test-results/runs/private/report.json': '{}',
      'frontend/test-results/.last-run-report.json': '{}',
      'frontend/test-results/runs/private/shard-1/test-results/failed/trace.zip': 'trace bytes',
      'frontend/playwright-report/index.html': '<html/>',
      'frontend/playwright-report/data/trace.zip': Buffer.from([0, 255, 128, 42]),
      'frontend/blob-report/report.zip': 'blob bytes',
      '.tmp/test-results/empty.log': '',
      '.tmp/test-results/space 日本語.log': 'unicode file',
    }
    writeFiles(root, files)
    writeFiles(root, {
      '.tmp/windows-command-job-private/payload.json': 'private environment',
      '.tmp/e2e-launcher-fixture-failed/frontend/node_modules/package/index.js': 'dependency',
      '.tmp/unrelated/report.json': 'unrelated',
    })
    expect(collectTestResultFiles(root)).toEqual(Object.keys(files).sort())
    const archive = await archiveTestResults({ root, name: 'test-results-fixture.tar.gz' })
    expect(readFileSync(archive).subarray(0, 2)).toEqual(Buffer.from([0x1F, 0x8B]))
    const destination = scratch.create('extracted-test-results')
    const extracted = spawnSync('tar', ['-xzf', archive, '-C', destination], { encoding: 'utf8' })
    expect(extracted.status, extracted.stderr).toBe(0)
    for (const [path, contents] of Object.entries(files))
      expect(readFileSync(join(destination, path))).toEqual(Buffer.from(contents))
    expect(existsSync(join(destination, '.tmp/windows-command-job-private'))).toBe(false)
    expect(readdirSync(dirname(archive))).toEqual(['test-results-fixture.tar.gz'])
  })

  it('skips empty runs and rejects unsafe archive names', async () => {
    const root = scratch.create('empty-test-results')
    expect(await archiveTestResults({ root })).toBeUndefined()
    writeFiles(root, { '.tmp/test-results/console.log': 'test output' })
    for (const name of [
      '../escape.tar.gz',
      '',
      'two\nlines.tar.gz',
      'file.zip',
    ])
      await expect(archiveTestResults({ root, name })).rejects.toThrow('archive name')
  })

  it('excludes symlinked report directories and files', () => {
    const root = scratch.create('test-results-symlinks')
    const outside = scratch.create('private-test-files')
    writeFiles(outside, { 'payload.json': 'private environment' })
    mkdirSync(join(root, '.tmp/test-results'), { recursive: true })
    symlinkSync(outside, join(root, '.tmp/test-results/private'), 'junction')
    if (process.platform !== 'win32')
      symlinkSync(join(outside, 'payload.json'), join(root, '.tmp/test-results/private.json'))
    expect(collectTestResultFiles(root)).toEqual([])
  })

  it.each(['.tmp', 'frontend'])('excludes a report with a symlinked ancestor: %s', (ancestor) => {
    const root = scratch.create('test-results-ancestor')
    const outside = scratch.create('private-ancestor')
    writeFiles(outside, { 'test-results/payload.json': 'private environment' })
    symlinkSync(outside, join(root, ancestor), 'junction')
    expect(collectTestResultFiles(root)).toEqual([])
  })

  it('keeps an earlier archive when compression fails', async () => {
    const root = scratch.create('test-results-archive-failure')
    writeFiles(root, { '.tmp/test-results/console.log': 'test output' })
    const previous = await archiveTestResults({ root })
    const contents = readFileSync(previous)
    await expect(archiveTestResults({
      root,
      write: async () => { throw new Error('fixture compression failure') },
    })).rejects.toThrow('fixture compression failure')
    expect(readFileSync(previous)).toEqual(contents)
    expect(readdirSync(dirname(previous))).toEqual(['test-results.tar.gz'])
  })

  it('creates archives through Node without platform executables', () => {
    const root = scratch.create('no-archive-executable')
    writeFiles(root, { '.tmp/test-results/日本語.log': 'report' })
    const module = pathToFileURL(resolve(import.meta.dirname, 'archive-test-results.mjs')).href
    const program = `import { archiveTestResults } from ${JSON.stringify(module)}; process.env.PATH=''; console.log(await archiveTestResults({ root: process.argv[1] }));`
    const result = spawnSync('node', [
      '--input-type=module',
      '-e',
      program,
      root,
    ], { encoding: 'utf8' })
    expect(result.status, result.stderr).toBe(0)
    expect(existsSync(result.stdout.trim())).toBe(true)
  })
})
