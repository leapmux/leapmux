import type { CommandProcess } from './e2eCommandProcess'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { basename, delimiter, dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { finishCleanup } from '../tests/e2e/helpers/cleanup'
import { stopTrackedProcesses } from '../tests/e2e/helpers/processRegistry'
import { copyRunBinary, LEAPMUX_BINARY_NAME } from '../tests/e2e/helpers/runBinary'
import { runCommand } from './e2eCommand'
import { writeNativeLastRunState } from './e2eLastRunReporter'
import { parseE2EOptions, serialRunArgs, shardSelectionArgs } from './e2eOptions'
import { assertMergedTestCoverage, collectShardBlobs, mergedJsonDestination, readDiscoveredTestCoverage, shardReporterEnvironment } from './e2eReports'
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
 * Build once. Each shard owns its servers and its browser.
 * A unit test supplies its own projectRoot so it cannot replace the real binary.
 */
export async function runE2E(args: string[], projectRoot: string = root): Promise<number> {
  const options = parseE2EOptions(args)
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
    const cwd = join(projectRoot, 'frontend')
    const outputRoot = resolve(cwd, options.outputDir ?? 'test-results')
    const parentLastRun = join(outputRoot, '.last-run.json')
    const outputFileDir = join(outputRoot, 'runs', `e2e-${basename(runDir).slice('e-'.length)}`)
    mkdirSync(outputFileDir, { recursive: true })
    if (options.serial) {
      runDirs.push(runDir)
      const serialEnv: NodeJS.ProcessEnv = {
        ...nativeRunEnvironment(env, runDir),
        LEAPMUX_E2E_OUTPUT_FILE_DIR: outputFileDir,
        // The native CLI gives --last-failed-file precedence over this environment value.
        PLAYWRIGHT_LAST_RUN_OUTPUT_FILE: env.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE || parentLastRun,
      }
      const code = await command('node', [playwrightCli, 'test', '--workers=1', '--retries=0', ...serialRunArgs(options.playwrightArgs, join(outputFileDir, 'test-results'))], { cwd, env: serialEnv }, { logPath: join(outputFileDir, 'console.log') })
      process.stdout.write(`E2E artifacts: ${outputFileDir}\n`)
      return interrupted || code
    }
    const selection = shardSelectionArgs(options.playwrightArgs)
    const discoveryDir = join(outputFileDir, 'discovery')
    mkdirSync(discoveryDir)
    const discoveryEnv = shardReporterEnvironment(env, discoveryDir)
    const discoveryCode = await command('node', [playwrightCli, 'test', '--list', '--reporter=json', '--pass-with-no-tests', '--workers=1', '--retries=0', ...selection], { cwd, env: discoveryEnv }, { logPath: join(discoveryDir, 'console.log'), label: 'discovery' })
    if (discoveryCode !== 0 || interrupted)
      return interrupted || discoveryCode
    const { files, cases } = readDiscoveredTestCoverage(discoveryEnv.PLAYWRIGHT_JSON_OUTPUT_FILE!)
    if (files.length === 0) {
      const passWithNoTests = selection.includes('--pass-with-no-tests')
      writeNativeLastRunState(parentLastRun, { status: passWithNoTests ? 'passed' : 'failed', failedTests: [] })
      if (!passWithNoTests)
        process.stderr.write('No selected E2E test files exist.\n')
      return passWithNoTests ? 0 : 1
    }
    const total = Math.min(options.workers, files.length)
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
      const testEnv = shardReporterEnvironment(nativeRunEnvironment(env, shardDir), shardArtifacts)
      commands.push(command('node', [playwrightCli, 'test', `--shard=${index}/${total}`, '--workers=1', '--retries=0', '--reporter=list,blob,json', '--pass-with-no-tests', ...selection], { cwd, env: testEnv }, { logPath: join(shardArtifacts, 'console.log'), label: `shard ${index}/${total}` }))
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
    mergedEnv.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE = parentLastRun
    const reporters = [...new Set([...(options.reporters ?? 'list').split(','), 'json', lastRunReporter])].join(',')
    const mergeCode = await command('node', [playwrightCli, 'merge-reports', `--reporter=${reporters}`, reports], { cwd, env: mergedEnv })
    if (interrupted || mergeCode !== 0)
      return interrupted || mergeCode
    assertMergedTestCoverage(mergedEnv.PLAYWRIGHT_JSON_OUTPUT_FILE, cases)
    process.stdout.write(`E2E artifacts: ${outputFileDir}\n`)
    process.stdout.write(`E2E combined JSON report: ${mergedEnv.PLAYWRIGHT_JSON_OUTPUT_FILE}\n`)
    if (options.reporters?.split(',').includes('json') && !process.env.PLAYWRIGHT_JSON_OUTPUT_FILE && !process.env.PLAYWRIGHT_JSON_OUTPUT_NAME)
      process.stdout.write(`${readFileSync(mergedEnv.PLAYWRIGHT_JSON_OUTPUT_FILE, 'utf8')}\n`)
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
