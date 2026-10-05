import type { Buffer } from 'node:buffer'
import type { ChildProcess } from 'node:child_process'
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { closeSync, existsSync, mkdirSync, mkdtempSync, openSync, readdirSync, readFileSync, rmSync, symlinkSync, watch, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { shardReporterEnvironment } from '../../../scripts/e2eReports'
import { isObject } from '../../../src/lib/jsonPick'
import { deferred } from '../../../src/test-support/async'
import { withCleanup } from './cleanup'
import { stopProcesses } from './process'

const require = createRequire(import.meta.url)
const scratch = resolve(import.meta.dirname, '../../../../.tmp')

const buildScript = String.raw`
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, renameSync, watch, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'

const records = join(process.cwd(), 'records');
const policy = JSON.parse(readFileSync(join(records, 'policy.json'), 'utf8'));
if (policy.holdBuild) {
  console.log('native-build-entered');
  const entry = join(records, 'build-entry.json');
  const draft = entry + '.writing';
  const processTable = process.platform === 'win32' ? null : execFileSync('ps', ['-o', 'pid=,ppid=,pgid=,command=', '-p', process.pid + ',' + process.ppid], { encoding: 'utf8' });
  writeFileSync(draft, JSON.stringify({ processId: process.pid, parentProcessId: process.ppid, processTable }));
  renameSync(draft, entry);
  await new Promise((accept, reject) => {
    const listener = watch(records, check);
    listener.once('error', error => {
      listener.close();
      reject(error);
    });
    function check() {
      if (existsSync(join(records, 'release-build'))) {
        listener.close();
        accept();
      }
    }
    check();
  });
}
const receipt = join(process.cwd(), 'build-count');
const count = existsSync(receipt) ? Number(readFileSync(receipt, 'utf8')) + 1 : 1;
writeFileSync(receipt, String(count));
writeFileSync(join(process.cwd(), process.platform === 'win32' ? 'leapmux.exe' : 'leapmux'), 'private-build-' + count);
`

const globalSetup = String.raw`
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { lstatSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'

function writeRecord(path, record) {
  const draft = path + '.writing-' + process.pid;
  writeFileSync(draft, JSON.stringify(record));
  renameSync(draft, path);
}

export default async function setup(config) {
  assert.equal(config.workers, 1);
  assert.equal(config.fullyParallel, false);
  assert.equal(process.env.E2E_STATE_PATH, undefined);
  const noncePath = process.env.LEAPMUX_E2E_NONCE_PATH;
  const nonce = process.env.LEAPMUX_E2E_NONCE;
  assert.ok(noncePath);
  assert.ok(nonce);
  assert.equal(readFileSync(noncePath, 'utf8'), nonce);
  const nativeRoot = dirname(noncePath);
  const binaryPath = join(nativeRoot, process.platform === 'win32' ? 'leapmux.exe' : 'leapmux');
  const records = resolve(process.cwd(), '../records');
  const buildCount = readFileSync(resolve(process.cwd(), '../build-count'), 'utf8');
  assert.equal(readFileSync(binaryPath, 'utf8'), 'private-build-' + buildCount);
  const server = createServer((request, response) => {
    const address = new URL(request.url, 'http://127.0.0.1');
    if (address.pathname === '/browser') {
      const label = address.searchParams.get('case');
      assert.ok(label === 'alpha' || label === 'beta');
      writeRecord(join(records, 'browser-' + label + '-' + nonce + '.json'), { label, nonce, processId: process.pid, url: request.url });
      response.writeHead(200, { 'Content-Type': 'text/html' });
      response.end('<!doctype html><html><body><h1>Native shard browser</h1><p data-testid="native-identity">' + nonce + '</p><button type="button" id="native-action">Run native action</button><output data-testid="native-result" id="native-result">idle</output><script>document.getElementById("native-action").addEventListener("click", () => { document.getElementById("native-result").textContent = "clicked:" + ' + JSON.stringify(nonce) + '; });</script></body></html>');
      return;
    }
    if (address.pathname === '/favicon.ico') {
      response.writeHead(204);
      response.end();
      return;
    }
    assert.equal(request.url, '/identity');
    response.writeHead(200, { 'Content-Type': 'application/json' });
    response.end(JSON.stringify({ nonce, nativeRoot, processId: process.pid }));
  });
  await new Promise((accept, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', accept);
  });
  const address = server.address();
  assert.ok(address && typeof address !== 'string');
  const statePath = join(nativeRoot, 'fixture-state.json');
  const state = { nativeRoot, nativeRootIsSymlink: lstatSync(nativeRoot).isSymbolicLink(), binaryPath, noncePath, nonce, processId: process.pid, port: address.port, statePath, workers: config.workers };
  writeRecord(statePath, state);
  process.env.E2E_STATE_PATH = statePath;
  writeRecord(join(records, 'setup-' + nonce + '.json'), state);
  return async () => {
    await new Promise((accept, reject) => server.close(error => error ? reject(error) : accept()));
    writeRecord(join(records, 'teardown-' + nonce + '.json'), { nonce, port: address.port });
  };
}
`

const fixtureRuntime = String.raw`
import { expect } from '@playwright/test'
import { fork } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, renameSync, watch, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'

function writeRecord(path, record) {
  const draft = path + '.writing-' + process.pid;
  writeFileSync(draft, JSON.stringify(record));
  renameSync(draft, path);
}

function waitForRelease(path) {
  return new Promise((accept, reject) => {
    const listener = watch(resolve(path, '..'), check);
    listener.once('error', error => {
      listener.close();
      reject(error);
    });
    function check() {
      if (existsSync(path)) {
        listener.close();
        accept();
      }
    }
    check();
  });
}

export async function runCase(label, testInfo, page) {
  const records = resolve(process.cwd(), '../records');
  const policy = JSON.parse(readFileSync(join(records, 'policy.json'), 'utf8'));
  const state = JSON.parse(readFileSync(process.env.E2E_STATE_PATH, 'utf8'));
  const response = await fetch('http://127.0.0.1:' + state.port + '/identity');
  expect(response.status).toBe(200);
  expect(await response.json()).toEqual({ nonce: state.nonce, nativeRoot: state.nativeRoot, processId: state.processId });
  let browserUrl;
  if (policy.browserTrace) {
    expect(page, 'the browser fixture receives its native Page').toBeDefined();
    browserUrl = 'http://127.0.0.1:' + state.port + '/browser?case=' + label;
    const navigation = await page.goto(browserUrl);
    expect(navigation?.status()).toBe(200);
    await expect(page.getByTestId('native-identity')).toHaveText(state.nonce);
    await page.getByRole('button', { name: 'Run native action' }).click();
    await expect(page.getByTestId('native-result')).toHaveText('clicked:' + state.nonce);
  }
  let nativeChildProcessId;
  if (policy.cancellation) {
    const child = fork(new URL('../native-child.mjs', import.meta.url), [], { stdio: ['ignore', 'ignore', 'ignore', 'ipc'] });
    expect(child.pid).toBeGreaterThan(0);
    nativeChildProcessId = child.pid;
    writeRecord(join(records, 'owned-child-' + label + '.json'), { nativeChildProcessId: child.pid });
    const registry = join(state.nativeRoot, 'processes');
    mkdirSync(registry);
    writeFileSync(join(registry, String(child.pid)), '');
    await new Promise((accept, reject) => {
      child.once('error', reject);
      child.once('message', message => message === 'ready' ? accept() : reject(new Error('The native fixture child did not become ready.')));
    });
  }
  const entered = { label, nonce: state.nonce, nativeRoot: state.nativeRoot, port: state.port, processId: state.processId, workerProcessId: process.pid, nativeChildProcessId, browserUrl, parallelIndex: testInfo.parallelIndex, started: process.hrtime.bigint().toString() };
  writeRecord(join(records, 'entry-' + label + '.json'), entered);
  await waitForRelease(join(records, 'release-' + label));
  const attachment = testInfo.outputPath('receipt.txt');
  writeFileSync(attachment, 'native-case-' + label);
  await testInfo.attach('isolation-receipt', { path: attachment, contentType: 'text/plain' });
  writeRecord(join(records, 'exit-' + label + '.json'), { label, finished: process.hrtime.bigint().toString() });
  expect((policy.failCases ?? []).includes(label), 'intentional fixture failure').toBe(false);
}
`

const nativeChild = String.raw`
import { createServer } from 'node:http'
import process from 'node:process'
createServer((request, response) => response.end('native fixture child')).listen(0, '127.0.0.1', () => process.send('ready'));
`

export interface NativeRecord {
  [key: string]: unknown
}

export interface NativeCase {
  id: string
  title: string
  file: string
  status: string
  results: unknown[]
}

export interface FixtureRun {
  root: string
  records: string
  report: NativeRecord
  cases: NativeCase[]
  code: number
  parallelRelease: boolean
  reportPath: string
  consolePath: string
}

/** Remove the private fixture only after its outer test passes. Preserve failed native evidence. */
export function cleanupNativeE2eFixture(root: string, outerTestPassed: boolean): void {
  if (!outerTestPassed)
    return
  rmSync(root, { recursive: true, force: true })
}

/** Keep the exact native case failures and retained paths in outer assertion output. */
export function nativeFixtureDiagnostics(run: FixtureRun): string {
  return JSON.stringify({
    root: run.root,
    reportPath: run.reportPath,
    consolePath: run.consolePath,
    errors: run.report.errors,
    cases: run.cases,
  }, null, 2)
}

export interface NativeCompletion {
  code: number | null
  signal: NodeJS.Signals | null
}

export function readNativeFixtureRecord(path: string): NativeRecord {
  const record: unknown = JSON.parse(readFileSync(path, 'utf8'))
  if (!isObject(record))
    throw new Error(`The native fixture record is not an object: ${path}`)
  return record
}

export function nativeFixtureStringField(record: NativeRecord, key: string): string {
  const value = record[key]
  if (typeof value !== 'string' || !value)
    throw new Error(`The native fixture record has no nonempty ${key}.`)
  return value
}

export function nativeFixtureCases(report: NativeRecord): NativeCase[] {
  const cases: NativeCase[] = []
  const visit = (suites: unknown): void => {
    if (!Array.isArray(suites))
      throw new Error('The native report has no suite array.')
    for (const suite of suites) {
      if (!isObject(suite) || !Array.isArray(suite.specs))
        throw new Error('The native report contains an incomplete suite.')
      for (const spec of suite.specs) {
        if (!isObject(spec) || !Array.isArray(spec.tests) || spec.tests.length !== 1)
          throw new Error('The native report must contain exactly one test per fixture case.')
        const test = spec.tests[0]
        if (!isObject(test) || !Array.isArray(test.results))
          throw new Error('The native report contains an incomplete test.')
        cases.push({ id: nativeFixtureStringField(spec, 'id'), title: nativeFixtureStringField(spec, 'title'), file: nativeFixtureStringField(spec, 'file'), status: nativeFixtureStringField(test, 'status'), results: test.results })
      }
      if (suite.suites !== undefined)
        visit(suite.suites)
    }
  }
  visit(report.suites)
  return cases.sort((left, right) => left.title.localeCompare(right.title))
}

export interface FixturePolicy {
  /** The fixture cases that fail on purpose: `alpha`, `beta`, or both. */
  failCases?: readonly string[]
  cancellation?: boolean
  holdBuild?: boolean
  browserTrace?: boolean
}

export function createNativeE2eFixture(policy: FixturePolicy = {}): string {
  mkdirSync(scratch, { recursive: true })
  const root = mkdtempSync(join(scratch, 'e2e-native-integration-'))
  const frontend = join(root, 'frontend')
  const tests = join(frontend, 'tests')
  const records = join(root, 'records')
  mkdirSync(tests, { recursive: true })
  mkdirSync(records)
  symlinkSync(resolve(import.meta.dirname, '../../../node_modules'), join(frontend, 'node_modules'), 'junction')
  writeFileSync(join(frontend, 'package.json'), JSON.stringify({ private: true, type: 'module' }))
  writeFileSync(join(root, 'Taskfile.yaml'), 'version: \'3\'\ntasks:\n  build-backend:\n    cmds:\n      - node build.mjs\n')
  writeFileSync(join(root, 'build.mjs'), buildScript)
  writeFileSync(join(records, 'policy.json'), JSON.stringify(policy))
  writeFileSync(join(frontend, 'playwright.config.mjs'), String.raw`
import { join } from 'node:path'
import process from 'node:process'
export default {
  testDir: './tests',
  testMatch: '**/*.spec.ts',
  globalSetup: './global-setup.mjs',
  workers: 1,
  fullyParallel: false,
  retries: 0,
  timeout: 10_000,
  use: ${policy.browserTrace ? `{ browserName: 'chromium', headless: true, trace: 'retain-on-failure' }` : '{}'},
  outputDir: join(process.env.LEAPMUX_E2E_OUTPUT_FILE_DIR ?? process.cwd(), 'test-results'),
};
`)
  writeFileSync(join(frontend, 'global-setup.mjs'), globalSetup)
  writeFileSync(join(frontend, 'native-child.mjs'), nativeChild)
  writeFileSync(join(tests, 'fixture.mjs'), fixtureRuntime)
  for (const label of ['alpha', 'beta']) {
    const browserFixture = policy.browserTrace ? '{ page }' : '{}'
    const browserArgument = policy.browserTrace ? ', page' : ''
    writeFileSync(join(tests, `${label}.spec.ts`), `import { test } from '@playwright/test'\nimport { runCase } from './fixture.mjs'\ntest('executes ${label}', async (${browserFixture}, testInfo) => runCase(${JSON.stringify(label)}, testInfo${browserArgument}))\n`)
  }
  return root
}

/** Release every waiting fixture process before cleanup can stop its owning runner. */
export function releaseNativeFixtureProcesses(records: string): void {
  for (const file of ['release-alpha', 'release-beta', 'release-build']) {
    const path = join(records, file)
    if (!existsSync(path))
      writeFileSync(path, 'release')
  }
}

interface NativeRunnerOptions {
  args?: string[]
  reportPath?: string
}

/** Start a private controller without changing the caller's E2E environment. */
export function startNativeE2eRunner(root: string, options: NativeRunnerOptions = {}): { child: ChildProcess, completion: Promise<NativeCompletion>, log: string } {
  const driver = join(root, 'run-e2e-driver.mjs')
  const runnerUrl = pathToFileURL(resolve(import.meta.dirname, '../../../scripts/run-e2e.ts')).href
  writeFileSync(driver, `
import process from 'node:process'
import { runE2E } from ${JSON.stringify(runnerUrl)}

// Windows cannot deliver a catchable POSIX signal. The control pipe invokes the runner's handler.
if (process.platform === 'win32')
  process.stdin.once('data', signal => process.emit(signal.toString().trim()));

try {
  process.exitCode = await runE2E(process.argv.slice(2), ${JSON.stringify(root)});
}
catch (error) {
  console.error(error);
  process.exitCode = 1;
}
finally {
  process.stdin.destroy();
}
`)
  const log = join(root, `runner-console-${randomUUID()}.log`)
  const descriptor = openSync(log, 'wx')
  const env = shardReporterEnvironment(process.env, join(root, 'controller-artifacts'))
  for (const key of ['LEAPMUX_E2E_OUTPUT_FILE_DIR', 'LEAPMUX_E2E_NONCE_PATH', 'LEAPMUX_E2E_NONCE', 'PLAYWRIGHT_LAST_RUN_OUTPUT_FILE'])
    delete env[key]
  env.PLAYWRIGHT_JSON_OUTPUT_FILE = options.reportPath ?? join(root, 'retained-artifacts', 'combined.json')
  env.E2E_STATE_PATH = join(root, 'foreign-state.json')
  env.PWDEBUG = '0'
  delete env.PWPAUSE
  let child: ChildProcess
  try {
    child = spawn('bun', [driver, ...(options.args ?? ['--workers=2', '--reporter=json', `--output=${join(root, 'retained-artifacts')}`])], {
      cwd: root,
      env,
      stdio: ['pipe', descriptor, descriptor],
    })
  }
  catch (error) {
    try {
      closeSync(descriptor)
    }
    catch (closeError) {
      throw new AggregateError([error, closeError], 'The native controller start and its log close failed.')
    }
    throw error
  }
  const completion = new Promise<NativeCompletion>((accept, reject) => {
    child.once('error', reject)
    child.once('close', (code, signal) => accept({ code, signal }))
  })
  // Cleanup can wait for a stop before it awaits this separate close promise.
  // Keep the original rejection available without an unhandled rejection during that wait.
  void completion.catch(() => {})
  try {
    closeSync(descriptor)
  }
  catch (error) {
    const failedCompletion = withCleanup(async (): Promise<NativeCompletion> => {
      throw error
    }, async () => {
      await stopProcesses([child])
      await completion
    })
    return { child, completion: failedCompletion, log }
  }
  return { child, completion, log }
}

export interface NativeFixtureRunOptions {
  workers: 1 | 2
  failCases?: readonly string[]
  args?: string[]
  reportPath?: string
  browserTrace?: boolean
  /**
   * The cases that the run selects. A rerun of the failures selects a subset.
   * The release wait ends when every expected case entered, so an unselected case cannot hold the run.
   */
  expectedCases?: readonly string[]
}

/** Run two controlled native cases and preserve their report after transient cleanup. */
export async function executeNativeE2eFixture(root: string, options: NativeFixtureRunOptions): Promise<FixtureRun> {
  const { workers, failCases } = options
  const records = join(root, 'records')
  for (const label of ['alpha', 'beta']) {
    for (const prefix of ['entry-', 'exit-', 'release-'])
      rmSync(join(records, `${prefix}${label}${prefix === 'release-' ? '' : '.json'}`), { force: true })
  }
  const policy = {
    ...(failCases === undefined ? {} : { failCases }),
    ...(options.browserTrace ? { browserTrace: true } : {}),
  }
  writeFileSync(join(records, 'policy.json'), JSON.stringify(policy))
  const expectedCases = options.expectedCases ?? ['alpha', 'beta']
  const output = join(root, 'retained-artifacts')
  const reportPath = options.reportPath ?? join(output, 'combined.json')
  mkdirSync(output, { recursive: true })
  let parallelRelease = false
  const watcherFailure = deferred<never>()
  const failWatcher = (error: unknown) => {
    try {
      releaseNativeFixtureProcesses(records)
    }
    catch (releaseError) {
      error = new AggregateError([error, releaseError], 'The native fixture watcher and case release failed.')
    }
    watcherFailure.reject(error)
  }
  const releaseCases = () => {
    try {
      if (parallelRelease)
        return
      const entries = readdirSync(records).filter(file => expectedCases.some(label => file === `entry-${label}.json`))
      if (workers === 2 && entries.length !== expectedCases.length)
        return
      if (workers === 2 && expectedCases.length > 1) {
        assert.deepEqual(readdirSync(records).filter(file => file.startsWith('exit-')), [])
        parallelRelease = true
      }
      for (const entry of entries) {
        const label = nativeFixtureStringField(readNativeFixtureRecord(join(records, entry)), 'label')
        const release = join(records, `release-${label}`)
        if (!existsSync(release))
          writeFileSync(release, 'release')
      }
    }
    catch (error) {
      failWatcher(error)
    }
  }
  const listener = watch(records, releaseCases)
  listener.once('error', failWatcher)
  let runner: ReturnType<typeof startNativeE2eRunner> | undefined
  return withCleanup(async () => {
    runner = startNativeE2eRunner(root, { args: [`--workers=${workers}`, '--reporter=json', `--output=${output}`, ...(options.args ?? [])], reportPath })
    const result = await Promise.race([runner.completion, watcherFailure.promise])
    if (result.code === null || result.signal !== null)
      throw new Error(`The private native controller exited through signal ${result.signal}.`)
    if (!existsSync(reportPath))
      throw new Error(`The private native controller produced no report.\n${readFileSync(runner.log, 'utf8')}`)
    const report = readNativeFixtureRecord(reportPath)
    return { root, records, report, cases: nativeFixtureCases(report), code: result.code, parallelRelease, reportPath, consolePath: runner.log }
  }, async () => {
    listener.close()
    releaseNativeFixtureProcesses(records)
    if (runner) {
      await stopProcesses([runner.child])
      await runner.completion
    }
  })
}

interface NativeZipArchive {
  entries: () => Promise<string[]>
  read: (entry: string) => Promise<Buffer>
  close: () => void
}

function isNativeZipConstructor(value: unknown): value is new (path: string) => NativeZipArchive {
  if (typeof value !== 'function')
    return false
  const prototype: unknown = value.prototype
  return isObject(prototype) && typeof prototype.entries === 'function' && typeof prototype.read === 'function' && typeof prototype.close === 'function'
}

/** Read archive entries with the same installed ZIP helper that native Playwright merge uses. */
export async function readNativeZipEntries(path: string): Promise<Map<string, Buffer>> {
  const core: unknown = require('playwright-core/lib/coreBundle')
  if (!isObject(core) || !isObject(core.utils) || !isNativeZipConstructor(core.utils.ZipFile))
    throw new Error('The installed Playwright ZIP helper has no valid archive interface.')
  const archive = new core.utils.ZipFile(path)
  return withCleanup(async () => {
    const entries = new Map<string, Buffer>()
    for (const entry of await archive.entries())
      entries.set(entry, await archive.read(entry))
    return entries
  }, async () => {
    archive.close()
  })
}
