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

/**
 * A fixture project for the tests of the E2E launcher, `scripts/run-e2e.ts`.
 *
 * The project is a private directory under `.tmp` with a Taskfile, a build script, and a Playwright project of two
 * cases, `alpha` and `beta`. The launcher builds it and runs it as it runs the real suite. The processes of the project
 * write JSON records into `records/`, so a test can read what each process saw and hold or release each case.
 */

const require = createRequire(import.meta.url)
const scratch = resolve(import.meta.dirname, '../../../../.tmp')

/** The name of the module of the project that waits for a record file. The build script and each case import it. */
export const WAIT_FOR_FILE_MODULE = 'wait-for-file.mjs'

/**
 * The time between two existence checks of a file wait, in milliseconds. It sets how late a wait sees a file whose
 * watch event was lost. It never sets when a wait ends: the wait ends when the file exists.
 */
const FILE_CHECK_INTERVAL_MS = 50

const waitForFileModule = String.raw`
import { existsSync, watch } from 'node:fs'
import { dirname } from 'node:path'

/**
 * Wait until the file at path exists. A test passes its own watchDirectory to control the watch events.
 *
 * A directory watch alone cannot end this wait. macOS starts the FSEvents stream of a watcher after watch() returns,
 * so a file that appears in that window gives no event, and the first check can run before the file appears. The
 * test side can write a release a few milliseconds after the entry record that it answers, which is inside that
 * window. The interval check finds such a file. The watcher only ends the wait sooner.
 */
export function waitForFile(path, watchDirectory = watch) {
  return new Promise((accept, reject) => {
    let settled = false;
    const listener = watchDirectory(dirname(path), check);
    const timer = setInterval(check, ${FILE_CHECK_INTERVAL_MS});
    listener.once('error', error => settle(() => reject(error)));
    function settle(action) {
      if (settled)
        return;
      settled = true;
      clearInterval(timer);
      listener.close();
      action();
    }
    function check() {
      if (existsSync(path))
        settle(accept);
    }
    check();
  });
}
`

const buildScript = String.raw`
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { waitForFile } from './${WAIT_FOR_FILE_MODULE}'

const records = join(process.cwd(), 'records');
const policy = JSON.parse(readFileSync(join(records, 'policy.json'), 'utf8'));
if (policy.holdBuild) {
  console.log('fixture-build-entered');
  const entry = join(records, 'build-entry.json');
  const draft = entry + '.writing';
  const processTable = process.platform === 'win32' ? null : execFileSync('ps', ['-o', 'pid=,ppid=,pgid=,command=', '-p', process.pid + ',' + process.ppid], { encoding: 'utf8' });
  writeFileSync(draft, JSON.stringify({ processId: process.pid, parentProcessId: process.ppid, processTable }));
  renameSync(draft, entry);
  await waitForFile(join(records, 'release-build'));
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
  const runDir = dirname(noncePath);
  const binaryPath = join(runDir, process.platform === 'win32' ? 'leapmux.exe' : 'leapmux');
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
      response.end('<!doctype html><html><body><h1>Fixture shard browser</h1><p data-testid="fixture-identity">' + nonce + '</p><button type="button" id="fixture-action">Run fixture action</button><output data-testid="fixture-result" id="fixture-result">idle</output><script>document.getElementById("fixture-action").addEventListener("click", () => { document.getElementById("fixture-result").textContent = "clicked:" + ' + JSON.stringify(nonce) + '; });</script></body></html>');
      return;
    }
    if (address.pathname === '/favicon.ico') {
      response.writeHead(204);
      response.end();
      return;
    }
    assert.equal(request.url, '/identity');
    response.writeHead(200, { 'Content-Type': 'application/json' });
    response.end(JSON.stringify({ nonce, runDir, processId: process.pid }));
  });
  await new Promise((accept, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', accept);
  });
  const address = server.address();
  assert.ok(address && typeof address !== 'string');
  const statePath = join(runDir, 'fixture-state.json');
  const state = { runDir, runDirIsSymlink: lstatSync(runDir).isSymbolicLink(), binaryPath, noncePath, nonce, processId: process.pid, port: address.port, statePath, workers: config.workers };
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
import { mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { waitForFile } from '../../${WAIT_FOR_FILE_MODULE}'

function writeRecord(path, record) {
  const draft = path + '.writing-' + process.pid;
  writeFileSync(draft, JSON.stringify(record));
  renameSync(draft, path);
}

export async function runCase(label, testInfo, page) {
  const records = resolve(process.cwd(), '../records');
  const policy = JSON.parse(readFileSync(join(records, 'policy.json'), 'utf8'));
  const state = JSON.parse(readFileSync(process.env.E2E_STATE_PATH, 'utf8'));
  const response = await fetch('http://127.0.0.1:' + state.port + '/identity');
  expect(response.status).toBe(200);
  expect(await response.json()).toEqual({ nonce: state.nonce, runDir: state.runDir, processId: state.processId });
  let browserUrl;
  if (policy.browserTrace) {
    expect(page, 'the browser fixture receives its Playwright Page').toBeDefined();
    browserUrl = 'http://127.0.0.1:' + state.port + '/browser?case=' + label;
    const navigation = await page.goto(browserUrl);
    expect(navigation?.status()).toBe(200);
    await expect(page.getByTestId('fixture-identity')).toHaveText(state.nonce);
    await page.getByRole('button', { name: 'Run fixture action' }).click();
    await expect(page.getByTestId('fixture-result')).toHaveText('clicked:' + state.nonce);
  }
  let ownedChildProcessId;
  if (policy.cancellation) {
    const child = fork(new URL('../owned-child.mjs', import.meta.url), [], { stdio: ['ignore', 'ignore', 'ignore', 'ipc'] });
    expect(child.pid).toBeGreaterThan(0);
    ownedChildProcessId = child.pid;
    writeRecord(join(records, 'owned-child-' + label + '.json'), { ownedChildProcessId: child.pid });
    const registry = join(state.runDir, 'processes');
    mkdirSync(registry);
    writeFileSync(join(registry, String(child.pid)), '');
    await new Promise((accept, reject) => {
      child.once('error', reject);
      child.once('message', message => message === 'ready' ? accept() : reject(new Error('The owned fixture child did not become ready.')));
    });
  }
  const entered = { label, nonce: state.nonce, runDir: state.runDir, port: state.port, processId: state.processId, workerProcessId: process.pid, ownedChildProcessId, browserUrl, parallelIndex: testInfo.parallelIndex, started: process.hrtime.bigint().toString() };
  writeRecord(join(records, 'entry-' + label + '.json'), entered);
  await waitForFile(join(records, 'release-' + label));
  const attachment = testInfo.outputPath('receipt.txt');
  writeFileSync(attachment, 'fixture-case-' + label);
  await testInfo.attach('isolation-receipt', { path: attachment, contentType: 'text/plain' });
  writeRecord(join(records, 'exit-' + label + '.json'), { label, finished: process.hrtime.bigint().toString() });
  expect((policy.failCases ?? []).includes(label), 'intentional fixture failure').toBe(false);
}
`

const ownedChildScript = String.raw`
import { createServer } from 'node:http'
import process from 'node:process'
createServer((request, response) => response.end('owned fixture child')).listen(0, '127.0.0.1', () => process.send('ready'));
`

export interface FixtureRecord {
  [key: string]: unknown
}

export interface FixtureCase {
  id: string
  title: string
  file: string
  status: string
  results: unknown[]
}

export interface FixtureRun {
  root: string
  records: string
  report: FixtureRecord
  cases: FixtureCase[]
  code: number
  parallelRelease: boolean
  reportPath: string
  consolePath: string
}

/** Remove the fixture project only after its outer test passes. Keep the evidence of a failed run. */
export function cleanupLauncherFixtureProject(root: string, outerTestPassed: boolean): void {
  if (!outerTestPassed)
    return
  rmSync(root, { recursive: true, force: true })
}

/** State the exact failures of the fixture cases and the retained paths in the output of an outer assertion. */
export function launcherFixtureDiagnostics(run: FixtureRun): string {
  return JSON.stringify({
    root: run.root,
    reportPath: run.reportPath,
    consolePath: run.consolePath,
    errors: run.report.errors,
    cases: run.cases,
  }, null, 2)
}

export interface LauncherCompletion {
  code: number | null
  signal: NodeJS.Signals | null
}

export function readFixtureRecord(path: string): FixtureRecord {
  const record: unknown = JSON.parse(readFileSync(path, 'utf8'))
  if (!isObject(record))
    throw new Error(`The fixture record is not an object: ${path}`)
  return record
}

export function fixtureStringField(record: FixtureRecord, key: string): string {
  const value = record[key]
  if (typeof value !== 'string' || !value)
    throw new Error(`The fixture record has no nonempty ${key}.`)
  return value
}

export function fixtureCases(report: FixtureRecord): FixtureCase[] {
  const cases: FixtureCase[] = []
  const visit = (suites: unknown): void => {
    if (!Array.isArray(suites))
      throw new Error('The fixture report has no suite array.')
    for (const suite of suites) {
      if (!isObject(suite) || !Array.isArray(suite.specs))
        throw new Error('The fixture report contains an incomplete suite.')
      for (const spec of suite.specs) {
        if (!isObject(spec) || !Array.isArray(spec.tests) || spec.tests.length !== 1)
          throw new Error('The fixture report must hold exactly one test for each fixture case.')
        const test = spec.tests[0]
        if (!isObject(test) || !Array.isArray(test.results))
          throw new Error('The fixture report contains an incomplete test.')
        cases.push({ id: fixtureStringField(spec, 'id'), title: fixtureStringField(spec, 'title'), file: fixtureStringField(spec, 'file'), status: fixtureStringField(test, 'status'), results: test.results })
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

export function createLauncherFixtureProject(policy: FixturePolicy = {}): string {
  mkdirSync(scratch, { recursive: true })
  const root = mkdtempSync(join(scratch, 'e2e-launcher-fixture-'))
  const frontend = join(root, 'frontend')
  const tests = join(frontend, 'tests')
  const records = join(root, 'records')
  mkdirSync(tests, { recursive: true })
  mkdirSync(records)
  symlinkSync(resolve(import.meta.dirname, '../../../node_modules'), join(frontend, 'node_modules'), 'junction')
  writeFileSync(join(frontend, 'package.json'), JSON.stringify({ private: true, type: 'module' }))
  writeFileSync(join(root, 'Taskfile.yaml'), 'version: \'3\'\ntasks:\n  build-backend:\n    cmds:\n      - node build.mjs\n')
  writeFileSync(join(root, WAIT_FOR_FILE_MODULE), waitForFileModule)
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
  writeFileSync(join(frontend, 'owned-child.mjs'), ownedChildScript)
  writeFileSync(join(tests, 'fixture.mjs'), fixtureRuntime)
  for (const label of ['alpha', 'beta']) {
    const browserFixture = policy.browserTrace ? '{ page }' : '{}'
    const browserArgument = policy.browserTrace ? ', page' : ''
    writeFileSync(join(tests, `${label}.spec.ts`), `import { test } from '@playwright/test'\nimport { runCase } from './fixture.mjs'\ntest('executes ${label}', async (${browserFixture}, testInfo) => runCase(${JSON.stringify(label)}, testInfo${browserArgument}))\n`)
  }
  return root
}

/** Release every waiting fixture process before the cleanup stops the launcher that owns it. */
export function releaseFixtureProcesses(records: string): void {
  for (const file of ['release-alpha', 'release-beta', 'release-build']) {
    const path = join(records, file)
    if (!existsSync(path))
      writeFileSync(path, 'release')
  }
}

interface LauncherOptions {
  args?: string[]
  reportPath?: string
}

/** Start a private launcher without a change to the E2E environment of the caller. */
export function startLauncher(root: string, options: LauncherOptions = {}): { child: ChildProcess, completion: Promise<LauncherCompletion>, log: string } {
  const driver = join(root, 'run-e2e-driver.mjs')
  const launcherUrl = pathToFileURL(resolve(import.meta.dirname, '../../../scripts/run-e2e.ts')).href
  writeFileSync(driver, `
import process from 'node:process'
import { runE2E } from ${JSON.stringify(launcherUrl)}

// Windows cannot deliver a catchable POSIX signal. The control pipe invokes the handler of the launcher.
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
  const log = join(root, `launcher-console-${randomUUID()}.log`)
  const descriptor = openSync(log, 'wx')
  const env = shardReporterEnvironment(process.env, join(root, 'launcher-artifacts'))
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
      throw new AggregateError([error, closeError], 'The launcher start and its log close failed.')
    }
    throw error
  }
  const completion = new Promise<LauncherCompletion>((accept, reject) => {
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
    const failedCompletion = withCleanup(async (): Promise<LauncherCompletion> => {
      throw error
    }, async () => {
      await stopProcesses([child])
      await completion
    })
    return { child, completion: failedCompletion, log }
  }
  return { child, completion, log }
}

export interface FixtureRunOptions {
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

/** Run the two controlled fixture cases, and keep their report after the transient cleanup. */
export async function runLauncherFixtureProject(root: string, options: FixtureRunOptions): Promise<FixtureRun> {
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
      releaseFixtureProcesses(records)
    }
    catch (releaseError) {
      error = new AggregateError([error, releaseError], 'The fixture watcher and the case release failed.')
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
        const label = fixtureStringField(readFixtureRecord(join(records, entry)), 'label')
        const release = join(records, `release-${label}`)
        if (!existsSync(release))
          writeFileSync(release, 'release')
      }
    }
    catch (error) {
      failWatcher(error)
    }
  }
  // `releaseCases` reads the complete records directory on each watch event and every FILE_CHECK_INTERVAL_MS, as
  // `waitForFile` in the fixture project checks for its file. A directory watch alone cannot release the cases. macOS
  // starts the FSEvents stream of a watcher after watch() returns, so an entry record that appears in that window gives
  // no event. The interval read finds such a record, so the release does not depend on the start order of the
  // processes. The watch only releases the cases sooner.
  const listener = watch(records, releaseCases)
  listener.once('error', failWatcher)
  const releaseCheck = setInterval(releaseCases, FILE_CHECK_INTERVAL_MS)
  let launcher: ReturnType<typeof startLauncher> | undefined
  return withCleanup(async () => {
    launcher = startLauncher(root, { args: [`--workers=${workers}`, '--reporter=json', `--output=${output}`, ...(options.args ?? [])], reportPath })
    const result = await Promise.race([launcher.completion, watcherFailure.promise])
    if (result.code === null || result.signal !== null)
      throw new Error(`The private launcher exited through signal ${result.signal}.`)
    if (!existsSync(reportPath))
      throw new Error(`The private launcher produced no report.\n${readFileSync(launcher.log, 'utf8')}`)
    const report = readFixtureRecord(reportPath)
    return { root, records, report, cases: fixtureCases(report), code: result.code, parallelRelease, reportPath, consolePath: launcher.log }
  }, async () => {
    clearInterval(releaseCheck)
    listener.close()
    releaseFixtureProcesses(records)
    if (launcher) {
      await stopProcesses([launcher.child])
      await launcher.completion
    }
  })
}

interface PlaywrightZipArchive {
  entries: () => Promise<string[]>
  read: (entry: string) => Promise<Buffer>
  close: () => void
}

function isPlaywrightZipConstructor(value: unknown): value is new (path: string) => PlaywrightZipArchive {
  if (typeof value !== 'function')
    return false
  const prototype: unknown = value.prototype
  return isObject(prototype) && typeof prototype.entries === 'function' && typeof prototype.read === 'function' && typeof prototype.close === 'function'
}

/** Read the entries of an archive with the installed ZIP helper that the merge of Playwright uses. */
export async function readPlaywrightZipEntries(path: string): Promise<Map<string, Buffer>> {
  const core: unknown = require('playwright-core/lib/coreBundle')
  if (!isObject(core) || !isObject(core.utils) || !isPlaywrightZipConstructor(core.utils.ZipFile))
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
