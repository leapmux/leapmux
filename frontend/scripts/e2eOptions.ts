import { availableParallelism } from 'node:os'
import process from 'node:process'

/** `history` assigns files to shards from recorded durations. `off` keeps Playwright's own `--shard=i/N` split. */
export type BalanceMode = 'history' | 'off'
const BALANCE_MODES = ['history', 'off'] as const satisfies readonly BalanceMode[]

function isBalanceMode(value: string): value is BalanceMode {
  return (BALANCE_MODES as readonly string[]).includes(value)
}

export interface E2EOptions {
  workers: number
  playwrightArgs: string[]
  reporters?: string
  outputDir?: string
  /** The caller selected --last-failed. */
  lastFailed: boolean
  /** The caller's --last-failed-file value, exactly as given. */
  lastFailedFile?: string
  /** The caller selected --failed-files or --failed-files-from. */
  failedFiles: boolean
  /** The caller's --failed-files-from value, exactly as given. */
  failedFilesFrom?: string
  /** The caller selected --pass-with-no-tests. */
  passWithNoTests: boolean
  /** The caller selected tests with --test-list. */
  testList: boolean
  balance: BalanceMode
  serial: boolean
}

const SERIAL_OPTIONS = new Set(['--debug', '--headed', '--help', '-h', '--list', '--ui', '--ui-host', '--ui-port', '--shard', '--config', '-c', '--max-failures', '-x', '--global-timeout', '--update-snapshots', '-u', '--update-source-method'])
/** Options whose next argument is always their value. The launcher consumes --workers, -j, --balance, and --failed-files-from. */
const VALUE_OPTIONS = new Set([
  '--workers',
  '-j',
  '--balance',
  '--failed-files-from',
  '--browser',
  '--reporter',
  '--output',
  '--retries',
  '--config',
  '-c',
  '--global-timeout',
  '--grep',
  '-g',
  '--grep-invert',
  '-G',
  '--last-failed-file',
  '--max-failures',
  '--project',
  '--repeat-each',
  '--run-agents',
  '--shard',
  '--test-list',
  '--test-list-invert',
  '--timeout',
  '--trace',
  '--tsconfig',
  '--ui-host',
  '--ui-port',
  '--update-source-method',
])
/** Playwright options with an optional value. Commander takes the next argument as the value when it is not an option. */
const OPTIONAL_VALUE_OPTIONS = new Set(['--debug', '--only-changed', '--update-snapshots', '-u'])
/** Playwright options that take a list. Commander adds every following argument that is not an option to the list. */
const VARIADIC_OPTIONS = new Set(['--project'])
/**
 * Options that select fewer tests of the selected files. They conflict with --failed-files, which reruns every test of its files.
 * --project is absent on purpose: playwright.config.ts defines one project, so a project filter keeps every test of a file,
 * and Playwright itself refuses an unknown project name.
 */
const NARROWING_OPTIONS = new Set(['--grep', '-g', '--grep-invert', '-G', '--test-list-invert', '--shard'])
/** Flags that the launcher reads. A value would change nothing in Playwright, so the launcher refuses one. */
const INSPECTED_FLAGS = new Set(['--last-failed', '--pass-with-no-tests', '--failed-files'])

/** Decide whether commander reads this argument as a value of the option before it. An option starts with a dash. */
function isValueArgument(argument: string | undefined): argument is string {
  return argument !== undefined && !argument.startsWith('-')
}

/** Expand the short Playwright options, and never read an attached value as another option. */
function shortArguments(argument: string): string[] {
  if (!argument.startsWith('-') || argument.startsWith('--') || argument.length < 3)
    return [argument]
  const result: string[] = []
  for (let index = 1; index < argument.length; index++) {
    const option = `-${argument[index]}`
    if (option === '-x' || option === '-h') {
      result.push(option)
      continue
    }
    if (option === '-u') {
      result.push(index + 1 < argument.length ? `--update-snapshots=${argument.slice(index + 1)}` : '-u')
      return result
    }
    if (VALUE_OPTIONS.has(option)) {
      result.push(option)
      if (index + 1 < argument.length)
        result.push(argument.slice(index + 1))
      return result
    }
    return [argument]
  }
  return result
}

/** Match Playwright's environment precedence. Console mode keeps the browser headless. */
function environmentInspector(): boolean {
  const mode = process.env.PWDEBUG ?? process.env.npm_config_pwdebug ?? process.env.npm_package_config_pwdebug ?? ''
  return !['', '0', 'false', 'console'].includes(mode)
}

/** Parse a positive worker count or percentage in Playwright's syntax, without numeric coercion. */
function workerCount(value: string, capacity: number): number {
  if (/^[1-9]\d*%$/u.test(value)) {
    const percentage = Number(value.slice(0, -1))
    if (percentage <= 100)
      return Math.max(1, Math.floor(capacity * percentage / 100))
  }
  else if (/^[1-9]\d*$/u.test(value) && Number.isSafeInteger(Number(value))) {
    return Number(value)
  }
  throw new Error('The E2E worker count must be a positive integer or a percentage from 1% through 100%.')
}

/** Refuse a selection that would not run the complete failed files. */
function requireCompleteFailedFiles(positionals: readonly string[], conflicts: { lastFailed: boolean, testList: boolean, onlyChanged: boolean, narrowing: string | undefined }): void {
  if (conflicts.lastFailed)
    throw new Error('The E2E --failed-files option reruns complete files, and --last-failed reruns only the failed tests. Use one of the two options.')
  if (positionals.length)
    throw new Error(`The E2E --failed-files option selects its own files. Remove the file arguments: ${positionals.join(' ')}`)
  if (conflicts.testList)
    throw new Error('The E2E --failed-files option selects its own files. Remove --test-list.')
  if (conflicts.onlyChanged)
    throw new Error('The E2E --failed-files option selects its own files. Remove --only-changed.')
  if (conflicts.narrowing !== undefined)
    throw new Error(`The E2E --failed-files option reruns every test of the failed files. Remove ${conflicts.narrowing}, which selects fewer tests.`)
}

/** Consume the public worker count and the launcher options. Every child keeps one Playwright worker. */
export function parseE2EOptions(args: readonly string[], capacity = availableParallelism()): E2EOptions {
  if (!Number.isSafeInteger(capacity) || capacity < 1)
    throw new Error('The E2E CPU capacity must be a positive integer.')
  let workers = Math.min(4, capacity)
  let workerOption = false
  let serial = environmentInspector()
  let reporters: string | undefined
  let outputDir: string | undefined
  let lastFailed = false
  let lastFailedFile: string | undefined
  let failedFiles = false
  let failedFilesFrom: string | undefined
  let passWithNoTests = false
  let testList = false
  let onlyChanged = false
  let narrowing: string | undefined
  let balance: BalanceMode | undefined
  const positionals: string[] = []
  const playwrightArgs: string[] = []
  const input = [...args]
  for (let index = 0; index < input.length; index++) {
    const expanded = shortArguments(input[index]!)
    if (expanded.length !== 1 || expanded[0] !== input[index])
      input.splice(index, 1, ...expanded)
    const argument = input[index]!
    if (argument === '--') {
      playwrightArgs.push(...input.slice(index))
      positionals.push(...input.slice(index + 1))
      break
    }
    if (!argument.startsWith('-') || argument === '-') {
      positionals.push(argument)
      playwrightArgs.push(argument)
      continue
    }
    const separator = argument.indexOf('=')
    const option = separator < 0 ? argument : argument.slice(0, separator)
    if (option === '--parallelism' || option === '--parellelism')
      throw new Error('Use --workers to select the number of isolated E2E shards.')
    if (option === '--fully-parallel')
      throw new Error('The E2E shared fixtures require serial tests inside each isolated shard. Use --workers instead of --fully-parallel.')
    if (SERIAL_OPTIONS.has(option))
      serial = true
    if (INSPECTED_FLAGS.has(option)) {
      if (separator >= 0)
        throw new Error(`The E2E ${option} option takes no value.`)
      if (option === '--last-failed')
        lastFailed = true
      else if (option === '--pass-with-no-tests')
        passWithNoTests = true
      else
        failedFiles = true
      // The launcher selects the failed files itself. Playwright has no such option.
      if (option !== '--failed-files')
        playwrightArgs.push(argument)
      continue
    }
    if (OPTIONAL_VALUE_OPTIONS.has(option)) {
      if (option === '--only-changed')
        onlyChanged = true
      playwrightArgs.push(argument)
      if (separator < 0 && isValueArgument(input[index + 1]))
        playwrightArgs.push(input[++index]!)
      continue
    }
    if (!VALUE_OPTIONS.has(option)) {
      playwrightArgs.push(argument)
      continue
    }
    const value = separator < 0 ? input[++index] : argument.slice(separator + 1)
    if (value === undefined || value.includes('\0'))
      throw new Error(`The E2E ${option} option requires a value without NUL.`)
    if (NARROWING_OPTIONS.has(option))
      narrowing ??= option
    if (option === '--workers' || option === '-j') {
      if (workerOption)
        throw new Error('The E2E worker count must appear only once.')
      workerOption = true
      workers = workerCount(value, capacity)
      continue
    }
    if (option === '--balance') {
      if (balance !== undefined)
        throw new Error('The E2E --balance option must appear only once.')
      if (!isBalanceMode(value))
        throw new Error('The E2E --balance option must be "history" or "off".')
      balance = value
      continue
    }
    if (option === '--failed-files-from') {
      if (!value)
        throw new Error('The E2E --failed-files-from option requires a nonempty report path.')
      if (failedFilesFrom !== undefined)
        throw new Error('The E2E --failed-files-from option must appear only once.')
      failedFiles = true
      failedFilesFrom = value
      continue
    }
    if (option === '--retries' && value !== '0')
      throw new Error('The E2E tests require zero retries. Remove the override or use --retries=0.')
    if (option === '--reporter') {
      if (!value)
        throw new Error('The E2E reporter option requires a nonempty value.')
      if (reporters !== undefined)
        throw new Error('The E2E reporter option must appear only once.')
      reporters = value
    }
    else if (option === '--output') {
      if (!value)
        throw new Error('The E2E output option requires a nonempty value.')
      if (outputDir !== undefined)
        throw new Error('The E2E output option must appear only once.')
      outputDir = value
    }
    else if (option === '--last-failed-file') {
      if (!value)
        throw new Error('The E2E --last-failed-file option requires a nonempty path.')
      if (lastFailedFile !== undefined)
        throw new Error('The E2E --last-failed-file option must appear only once.')
      lastFailedFile = value
    }
    else if (option === '--test-list') {
      testList = true
    }
    playwrightArgs.push(argument)
    if (separator < 0)
      playwrightArgs.push(value)
    if (VARIADIC_OPTIONS.has(option)) {
      while (isValueArgument(input[index + 1]))
        playwrightArgs.push(input[++index]!)
    }
  }
  if (failedFiles)
    requireCompleteFailedFiles(positionals, { lastFailed, testList, onlyChanged, narrowing })
  return {
    workers: serial ? 1 : workers,
    playwrightArgs,
    ...(reporters === undefined ? {} : { reporters }),
    ...(outputDir === undefined ? {} : { outputDir }),
    lastFailed,
    ...(lastFailedFile === undefined ? {} : { lastFailedFile }),
    failedFiles,
    ...(failedFilesFrom === undefined ? {} : { failedFilesFrom }),
    passWithNoTests,
    testList,
    balance: balance ?? 'history',
    serial: serial || workers === 1,
  }
}

/**
 * Remove actual options while preserving values that resemble those options.
 * Commander gives the next arguments that are not options to a bare optional-value option and to a list option.
 * A removed option can be the one that separated such an option from a file filter, so the filter would become its value.
 * The function therefore moves these options after every other argument and before any `--`.
 * An optional-value option that has its value stays in place, because it takes no further argument.
 */
function withoutRunOptions(args: readonly string[], removed: ReadonlySet<string>): string[] {
  const result: string[] = []
  const last: string[] = []
  for (let index = 0; index < args.length; index++) {
    const argument = args[index]!
    if (argument === '--')
      return [...result, ...last, ...args.slice(index)]
    const option = argument.split('=', 1)[0]
    const takesValue = option !== undefined && VALUE_OPTIONS.has(option) && !argument.includes('=')
    if (option !== undefined && removed.has(option)) {
      // A removed flag has no value. Keep the next argument, which can be a file filter.
      if (takesValue)
        index++
      continue
    }
    if (option !== undefined && VARIADIC_OPTIONS.has(option)) {
      last.push(argument)
      if (takesValue)
        last.push(args[++index]!)
      while (isValueArgument(args[index + 1]))
        last.push(args[++index]!)
      continue
    }
    if (option !== undefined && OPTIONAL_VALUE_OPTIONS.has(option) && !argument.includes('=')) {
      if (isValueArgument(args[index + 1]))
        result.push(argument, args[++index]!)
      else
        last.push(argument)
      continue
    }
    result.push(argument)
    if (takesValue)
      result.push(args[++index]!)
  }
  return [...result, ...last]
}

/**
 * Keep every caller filter for discovery and the shards.
 * Remove the destinations and the last-run selection, because the launcher supplies a private copy of each one.
 */
export function shardSelectionArgs(args: readonly string[]): string[] {
  return withoutRunOptions(args, new Set(['--reporter', '--output', '--last-failed', '--last-failed-file']))
}

/** Keep serial test artifacts inside this run without changing reporters or test filters. */
export function serialRunArgs(args: readonly string[], outputDir: string, testList?: string): string[] {
  return [`--output=${outputDir}`, ...testListArgs(testList), ...withoutRunOptions(args, new Set(['--output']))]
}

export interface ChildSelection {
  /** The caller filters from shardSelectionArgs. */
  readonly filters: readonly string[]
  /** A run-private copy of the --last-failed state. Present only when the caller selected --last-failed. */
  readonly lastFailedFile?: string
  /** A test list that limits the child to exact files. */
  readonly testList?: string
}

function testListArgs(testList: string | undefined): string[] {
  return testList === undefined ? [] : [`--test-list=${testList}`]
}

/** The launcher options precede the caller filters, which can end with `--` and positional filters. */
function childSelectionArgs(selection: ChildSelection): string[] {
  return [
    ...testListArgs(selection.testList),
    ...(selection.lastFailedFile === undefined ? [] : ['--last-failed', `--last-failed-file=${selection.lastFailedFile}`]),
    ...selection.filters,
  ]
}

/** List the selected cases once, with the same selection that the shards receive. */
export function discoveryRunArgs(selection: ChildSelection): string[] {
  return ['--list', '--reporter=json', '--pass-with-no-tests', '--workers=1', '--retries=0', ...childSelectionArgs(selection)]
}

export interface StaticShard {
  readonly index: number
  readonly total: number
}

/**
 * Run one shard. A static shard uses Playwright's own `--shard=i/N` split of the selection.
 * A balanced shard gives its exact files in `selection.testList` instead.
 */
export function shardRunArgs(selection: ChildSelection, shard?: StaticShard): string[] {
  return [
    ...(shard === undefined ? [] : [`--shard=${shard.index}/${shard.total}`]),
    '--workers=1',
    '--retries=0',
    '--reporter=list,blob,json',
    '--pass-with-no-tests',
    ...childSelectionArgs(selection),
  ]
}
