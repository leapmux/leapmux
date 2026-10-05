import type { CommandProcess } from './e2eCommandProcess'
import type { ChildSelection } from './e2eOptions'
import { constants, copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { basename, delimiter, dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { inspect } from 'node:util'
import { finishCleanup } from '../tests/e2e/helpers/cleanup'
import { stopTrackedProcesses } from '../tests/e2e/helpers/processRegistry'
import { copyRunBinary, LEAPMUX_BINARY_NAME } from '../tests/e2e/helpers/runBinary'
import { runCommand } from './e2eCommand'
import { lastRunStatePath, readLastFailedState } from './e2eLastRunReporter'
import { discoveryRunArgs, parseE2EOptions, serialRunArgs, shardRunArgs, shardSelectionArgs } from './e2eOptions'
import { assertMergedTestCoverage, assertReportIsCurrent, collectShardBlobs, LAST_RUN_REPORT_FILE, mergedJsonDestination, readDiscoveredTestCoverage, readFailedReportFiles, reportedFileDurations, shardReporterEnvironment } from './e2eReports'
import { DURATION_HISTORY_FILE, formatShardPlan, mergeDurationHistory, planShards, readDurationHistory, testListContent, writeDurationHistory } from './e2eShardPlan'
import { writeFileAtomically } from './e2eStateFiles'
import { resolveTaskBin } from './resolve-task-bin'

const require = createRequire(import.meta.url)
const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const playwrightCli = require.resolve('@playwright/test/cli')
const lastRunReporter = fileURLToPath(new URL('./e2eLastRunReporter.ts', import.meta.url))

function nativeRunEnvironment(env: NodeJS.ProcessEnv, runDir: string): NodeJS.ProcessEnv {
  const noncePath = join(runDir, 'nonce')
  const nonce = crypto.randomUUID()
  // Global setup verifies this nonce before it starts any fixture.
  writeFileSync(noncePath, nonce)
  const result: NodeJS.ProcessEnv = {
    ...env,
    E2E_STATE_PATH: undefined,
    LEAPMUX_E2E_OUTPUT_FILE_DIR: undefined,
    LEAPMUX_E2E_NONCE_PATH: noncePath,
    LEAPMUX_E2E_NONCE: nonce,
    // A private directory under .tmp must not inherit the project's Git repository.
    GIT_CEILING_DIRECTORIES: [runDir, env.GIT_CEILING_DIRECTORIES].filter(Boolean).join(delimiter),
  }
  for (const key of ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_COMMON_DIR', 'GIT_INDEX_FILE', 'GIT_OBJECT_DIRECTORY', 'GIT_ALTERNATE_OBJECT_DIRECTORIES'])
    delete result[key]
  return result
}

/** Retain failure groups while removing repeated copies of the same underlying error. */
function distinctFailures(errors: readonly unknown[]): unknown[] {
  const seen = new Set<unknown>()
  const groups = new Set<AggregateError>()
  const visit = (error: unknown): unknown[] => {
    if (error instanceof AggregateError && error.errors.length) {
      if (groups.has(error))
        return []
      groups.add(error)
      const original: unknown[] = error.errors
      const children = original.flatMap(visit)
      if (children.length === 0)
        return []
      if (children.length === original.length && children.every((child, index) => child === original[index]))
        return [error]
      return [new AggregateError(children, error.message)]
    }
    if (seen.has(error))
      return []
    seen.add(error)
    return [error]
  }
  return errors.flatMap(visit)
}

/**
 * Finish a run that selects no test.
 * No test ran, so the run leaves the saved last-run state as it is. A mistyped filter then cannot erase the saved failures.
 * Native Playwright replaces the state with an empty one, and a serial run keeps that native behavior.
 */
function finishWithoutTests(passWithNoTests: boolean, reason: string): number {
  if (passWithNoTests) {
    process.stdout.write(`${reason}\n`)
    return 0
  }
  process.stderr.write(`${reason} Add --pass-with-no-tests to accept an empty selection.\n`)
  return 1
}

/**
 * Save the measured file durations for the next shard plan.
 * Read the saved history again, because another run can share the output root and end while this run goes on.
 * The history only balances later runs. A failure to save it does not change this run's result, so report it as a warning.
 */
function recordDurationHistory(path: string, report: unknown): void {
  try {
    writeDurationHistory(path, mergeDurationHistory(readDurationHistory(path), reportedFileDurations(report)))
  }
  catch (error) {
    process.stderr.write(`Warning: the E2E duration history at ${path} was not saved. A later run can use the native shard split.\n${inspect(error)}\n`)
  }
}

/**
 * Build once. Each shard owns its servers and its browser.
 * A unit test supplies its own projectRoot so it cannot replace the real binary.
 */
export async function runE2E(args: string[], projectRoot: string = root): Promise<number> {
  const options = parseE2EOptions(args)
  const cwd = join(projectRoot, 'frontend')
  const outputRoot = resolve(cwd, options.outputDir ?? 'test-results')
  const parentLastRun = join(outputRoot, '.last-run.json')
  const lastRunState = lastRunStatePath(options.lastFailedFile, process.env, cwd, parentLastRun)
  const lastRunReport = join(outputRoot, LAST_RUN_REPORT_FILE)
  const historyPath = join(outputRoot, DURATION_HISTORY_FILE)
  // Read each saved selection before the build, so an absent, malformed, or empty selection costs no build.
  const lastFailed = options.lastFailed ? readLastFailedState(lastRunState) : undefined
  // A serial run keeps the native result for an empty selection: native Playwright writes its own reports.
  if (lastFailed?.failedTests.length === 0 && !options.serial)
    return finishWithoutTests(options.passWithNoTests, `The last-run state at ${lastRunState} lists no failed tests.`)
  let failedFiles: { report: string, files: string[], testList: string } | undefined
  if (options.failedFiles) {
    const report = options.failedFilesFrom === undefined ? lastRunReport : resolve(cwd, options.failedFilesFrom)
    const files = readFailedReportFiles(report)
    // The caller chooses an explicit report. The saved report must describe the last run, which a serial run does not save.
    if (options.failedFilesFrom === undefined)
      assertReportIsCurrent(report, lastRunState)
    if (files.length === 0) {
      process.stdout.write(`No failed E2E test files exist in ${report}.\n`)
      return 0
    }
    failedFiles = { report, files, testList: testListContent(files) }
  }
  const owned = new Set<CommandProcess>()
  const directories = new Map<CommandProcess, string>()
  const pending = new Map<Promise<number>, CommandProcess | undefined>()
  const runDirs: string[] = []
  let runDir = ''
  let interrupted = 0
  let stopping = Promise.resolve()
  let resolveStopped!: (code: number) => void
  let rejectFailedStop!: (error: unknown) => void
  const signalStopped = new Promise<number>((resolve, reject) => {
    resolveStopped = resolve
    rejectFailedStop = reject
  })
  const command = (...parameters: Parameters<typeof runCommand>) => {
    const [name, argv, launch = {}, runtime = {}] = parameters
    let owner: CommandProcess | undefined
    const result = runCommand(name, argv, launch, {
      ...runtime,
      observe: (ownedProcess) => {
        owner = ownedProcess
        owned.add(ownedProcess)
        const nonce = launch.env?.LEAPMUX_E2E_NONCE_PATH
        if (nonce)
          directories.set(ownedProcess, dirname(nonce))
        runtime.observe?.(ownedProcess)
      },
    })
    pending.set(result, owner)
    void result.then(() => pending.delete(result), () => pending.delete(result))
    const completion = Promise.race([result, signalStopped])
    // Partial launch can fail before its caller reaches the command wait.
    void completion.catch(() => {})
    return completion
  }
  const stop = (code: number) => {
    if (interrupted)
      return
    interrupted = code
    stopping = finishCleanup([...owned].map(async command => command.stop()))
    void stopping.then(() => resolveStopped(interrupted), rejectFailedStop)
  }
  const interrupt = () => stop(130)
  const terminate = () => stop(143)
  process.on('SIGINT', interrupt)
  process.on('SIGTERM', terminate)
  const operation = async (): Promise<number> => {
    // The build cache includes this flag, which enables browser timing events.
    const env: NodeJS.ProcessEnv = {
      ...process.env,
      LEAPMUX_DEV: '1',
      E2E_STATE_PATH: undefined,
      LEAPMUX_E2E_OUTPUT_FILE_DIR: undefined,
      LEAPMUX_E2E_NONCE_PATH: undefined,
      LEAPMUX_E2E_NONCE: undefined,
    }
    const buildCode = await command(resolveTaskBin(), ['build-backend'], { cwd: projectRoot, env }, { ownership: { ownTree: true } })
    if (interrupted || buildCode !== 0)
      return interrupted || buildCode
    const scratch = join(projectRoot, '.tmp')
    mkdirSync(scratch, { recursive: true })
    // Native providers create UUID socket paths under this actual runtime root.
    runDir = mkdtempSync(join(scratch, 'e-'))
    // Copy the binary before another Task pipeline can replace the root build.
    const binary = copyRunBinary(join(projectRoot, LEAPMUX_BINARY_NAME), runDir)
    const outputFileDir = join(outputRoot, 'runs', `e2e-${basename(runDir).slice('e-'.length)}`)
    mkdirSync(outputFileDir, { recursive: true })
    let failedFilesList: string | undefined
    if (failedFiles) {
      failedFilesList = join(outputFileDir, 'failed-files.txt')
      writeFileSync(failedFilesList, failedFiles.testList, { flag: 'wx' })
      process.stdout.write(`E2E --failed-files: ${failedFiles.files.length} ${failedFiles.files.length === 1 ? 'file' : 'files'} from ${failedFiles.report}\n`)
    }
    if (options.serial) {
      runDirs.push(runDir)
      const serialEnv: NodeJS.ProcessEnv = {
        ...nativeRunEnvironment(env, runDir),
        LEAPMUX_E2E_OUTPUT_FILE_DIR: outputFileDir,
        // The native CLI gives --last-failed-file precedence over this environment value.
        PLAYWRIGHT_LAST_RUN_OUTPUT_FILE: env.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE || parentLastRun,
      }
      const code = await command('node', [playwrightCli, 'test', '--workers=1', '--retries=0', ...serialRunArgs(options.playwrightArgs, join(outputFileDir, 'test-results'), failedFilesList)], { cwd, env: serialEnv }, { logPath: join(outputFileDir, 'console.log') })
      process.stdout.write(`E2E artifacts: ${outputFileDir}\n`)
      return interrupted || code
    }
    // Native Playwright writes its last-run result back to the file that it reads.
    // Each child therefore reads a private copy of one snapshot, and only the merge writes the caller's state.
    let lastFailedSnapshot: string | undefined
    if (lastFailed) {
      lastFailedSnapshot = join(outputFileDir, 'last-failed.json')
      writeFileSync(lastFailedSnapshot, lastFailed.content, { flag: 'wx' })
    }
    const filters = shardSelectionArgs(options.playwrightArgs)
    const selection: ChildSelection = {
      filters,
      ...(lastFailedSnapshot === undefined ? {} : { lastFailedFile: lastFailedSnapshot }),
      ...(failedFilesList === undefined ? {} : { testList: failedFilesList }),
    }
    const discoveryDir = join(outputFileDir, 'discovery')
    mkdirSync(discoveryDir)
    const discoveryEnv = shardReporterEnvironment(env, discoveryDir)
    const discoveryCode = await command('node', [playwrightCli, 'test', ...discoveryRunArgs(selection)], { cwd, env: discoveryEnv }, { logPath: join(discoveryDir, 'console.log'), label: 'discovery' })
    if (discoveryCode !== 0 || interrupted)
      return interrupted || discoveryCode
    const coverage = readDiscoveredTestCoverage(discoveryEnv.PLAYWRIGHT_JSON_OUTPUT_FILE!)
    if (failedFiles) {
      const discovered = new Set(coverage.files)
      const absent = failedFiles.files.filter(file => !discovered.has(file))
      if (absent.length)
        process.stderr.write(`Warning: these failed files from ${failedFiles.report} select no test now: ${absent.join(', ')}\n`)
    }
    if (coverage.files.length === 0)
      return finishWithoutTests(options.passWithNoTests, lastFailed ? `No selected E2E test matches a failed test in ${lastRunState}.` : 'No selected E2E test files exist.')
    const history = readDurationHistory(historyPath)
    const plan = planShards({ coverage, workers: options.workers, balance: options.balance, history, callerTestList: options.testList })
    process.stdout.write(formatShardPlan(plan, historyPath))
    const total = plan.kind === 'static' ? plan.total : plan.shards.length
    const artifacts: string[] = []
    const commands: Promise<number>[] = []
    for (let index = 1; index <= total; index++) {
      if (interrupted)
        break
      const shardDir = join(runDir, String(index))
      const shardArtifacts = join(outputFileDir, `shard-${index}`)
      mkdirSync(shardDir)
      runDirs.push(shardDir)
      mkdirSync(shardArtifacts)
      copyRunBinary(binary, shardDir)
      artifacts.push(shardArtifacts)
      let shardSelection: ChildSelection = { filters, ...(failedFilesList === undefined ? {} : { testList: failedFilesList }) }
      if (plan.kind === 'balanced') {
        // The shard's own list replaces the failed-file list: native Playwright accepts one test list, and the shard's files are a subset.
        const testList = join(shardArtifacts, 'test-list.txt')
        writeFileSync(testList, testListContent(plan.shards[index - 1]!.files.map(file => file.file)), { flag: 'wx' })
        shardSelection = { filters, testList }
      }
      if (lastFailedSnapshot !== undefined) {
        // Native Playwright replaces this copy with the shard's own result when the shard ends.
        const lastFailedFile = join(shardArtifacts, 'last-run.json')
        copyFileSync(lastFailedSnapshot, lastFailedFile, constants.COPYFILE_EXCL)
        shardSelection = { ...shardSelection, lastFailedFile }
      }
      const testEnv = shardReporterEnvironment(nativeRunEnvironment(env, shardDir), shardArtifacts)
      const shardArgs = shardRunArgs(shardSelection, plan.kind === 'static' ? { index, total } : undefined)
      commands.push(command('node', [playwrightCli, 'test', ...shardArgs], { cwd, env: testEnv }, { logPath: join(shardArtifacts, 'console.log'), label: `shard ${index}/${total}` }))
    }
    const results = await Promise.allSettled(commands)
    await stopping
    if (interrupted)
      return interrupted
    const failures = results.flatMap(result => result.status === 'rejected' ? [result.reason] : [])
    if (failures.length)
      throw new AggregateError(failures, 'One or more E2E shard processes failed to run.')
    const code = results.find(result => result.status === 'fulfilled' && result.value !== 0)
    const reports = join(outputFileDir, 'combined-blobs')
    collectShardBlobs(artifacts, reports)
    const mergedEnv = shardReporterEnvironment(env, outputFileDir)
    mergedEnv.PLAYWRIGHT_JSON_OUTPUT_FILE = mergedJsonDestination(process.env, cwd, outputFileDir)
    mergedEnv.PLAYWRIGHT_BLOB_OUTPUT_DIR = join(outputFileDir, 'merged-blob-report')
    mergedEnv.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE = lastRunState
    const reporters = [...new Set([...(options.reporters ?? 'list').split(','), 'json', lastRunReporter])].join(',')
    const mergeCode = await command('node', [playwrightCli, 'merge-reports', `--reporter=${reporters}`, reports], { cwd, env: mergedEnv })
    if (interrupted || mergeCode !== 0)
      return interrupted || mergeCode
    const mergedReportPath = mergedEnv.PLAYWRIGHT_JSON_OUTPUT_FILE
    const mergedText = readFileSync(mergedReportPath, 'utf8')
    const merged: unknown = JSON.parse(mergedText)
    recordDurationHistory(historyPath, merged)
    assertMergedTestCoverage(merged, coverage.cases)
    // Keep the last combined report at a fixed path for --failed-files, as the merge keeps the last-run state.
    // Save it only after the coverage check: a report that misses a selected case must not select the next rerun.
    writeFileAtomically(lastRunReport, mergedText)
    process.stdout.write(`E2E artifacts: ${outputFileDir}\n`)
    process.stdout.write(`E2E combined JSON report: ${mergedReportPath}\n`)
    if (options.reporters?.split(',').includes('json') && !process.env.PLAYWRIGHT_JSON_OUTPUT_FILE && !process.env.PLAYWRIGHT_JSON_OUTPUT_NAME)
      process.stdout.write(`${mergedText}\n`)
    return code?.status === 'fulfilled' ? code.value : 0
  }

  const cleanup = async (): Promise<unknown[]> => {
    const failures: unknown[] = []
    const commands = [...owned]
    const stopped = await Promise.allSettled(commands.map(async command => command.stop()))
    const failedCommands = new Set<CommandProcess>()
    for (const [index, result] of stopped.entries()) {
      if (result.status === 'rejected') {
        failures.push(result.reason)
        failedCommands.add(commands[index]!)
      }
    }
    try {
      await stopping
    }
    catch (error) {
      failures.push(error)
    }
    const retained = new Set([...failedCommands].flatMap((command) => {
      const directory = directories.get(command)
      return directory ? [directory] : []
    }))
    // A recorded descendant can retain stdout after the command root exits.
    // Stop those descendants before waiting for the command's stream-close event.
    const native = await Promise.allSettled(runDirs.map(directory => stopTrackedProcesses(directory)))
    for (const [index, result] of native.entries()) {
      if (result.status === 'rejected') {
        failures.push(result.reason)
        retained.add(runDirs[index]!)
      }
    }
    const completed = await Promise.allSettled([...pending].flatMap(([result, owner]) => {
      const directory = owner ? directories.get(owner) : undefined
      return (owner && failedCommands.has(owner)) || (directory && retained.has(directory)) ? [] : [result]
    }))
    for (const result of completed) {
      if (result.status === 'rejected')
        failures.push(result.reason)
    }
    for (const directory of runDirs) {
      if (retained.has(directory))
        continue
      try {
        rmSync(directory, { recursive: true, force: true })
      }
      catch (error) {
        failures.push(error)
      }
    }
    if (runDir && failures.length === 0) {
      try {
        rmSync(runDir, { recursive: true, force: true })
      }
      catch (error) {
        failures.push(error)
      }
    }
    return failures
  }

  try {
    const outcome = await operation().then(
      code => ({ status: 'completed' as const, code }),
      (error: unknown) => ({ status: 'failed' as const, error }),
    )
    const cleanupFailures = await cleanup()
    if (cleanupFailures.length) {
      const failures = distinctFailures([
        ...(outcome.status === 'failed' ? [outcome.error] : []),
        ...cleanupFailures,
      ])
      throw new AggregateError(failures, 'Test cleanup failed')
    }
    if (outcome.status === 'failed')
      throw outcome.error
    return interrupted || outcome.code
  }
  finally {
    process.off('SIGINT', interrupt)
    process.off('SIGTERM', terminate)
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    process.exitCode = await runE2E(process.argv.slice(2))
  }
  catch (error) {
    console.error(error)
    process.exitCode = 1
  }
}
