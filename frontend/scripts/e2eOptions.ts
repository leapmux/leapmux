import { availableParallelism } from 'node:os'
import process from 'node:process'

export interface E2EOptions {
  workers: number
  playwrightArgs: string[]
  reporters?: string
  outputDir?: string
  serial: boolean
}

const SERIAL_OPTIONS = new Set(['--debug', '--headed', '--help', '-h', '--list', '--ui', '--ui-host', '--ui-port', '--last-failed', '--last-failed-file', '--shard', '--config', '-c', '--max-failures', '-x', '--global-timeout', '--update-snapshots', '-u', '--update-source-method'])
const VALUE_OPTIONS = new Set([
  '--workers',
  '-j',
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

/** Expand native short options without interpreting their attached value as another option. */
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

/** Match native environment precedence. Console mode keeps the browser headless. */
function environmentInspector(): boolean {
  const mode = process.env.PWDEBUG ?? process.env.npm_config_pwdebug ?? process.env.npm_package_config_pwdebug ?? ''
  return !['', '0', 'false', 'console'].includes(mode)
}

/** Parse a positive native worker count or percentage without numeric coercion. */
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

/** Consume the public worker count. Every child retains one native Playwright worker. */
export function parseE2EOptions(args: readonly string[], capacity = availableParallelism()): E2EOptions {
  if (!Number.isSafeInteger(capacity) || capacity < 1)
    throw new Error('The E2E CPU capacity must be a positive integer.')
  let workers = Math.min(4, capacity)
  let workerOption = false
  let serial = environmentInspector() || !!process.env.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE
  let reporters: string | undefined
  let outputDir: string | undefined
  const playwrightArgs: string[] = []
  const input = [...args]
  for (let index = 0; index < input.length; index++) {
    const expanded = shortArguments(input[index]!)
    if (expanded.length !== 1 || expanded[0] !== input[index])
      input.splice(index, 1, ...expanded)
    const argument = input[index]!
    if (argument === '--') {
      playwrightArgs.push(...input.slice(index))
      break
    }
    const separator = argument.indexOf('=')
    const option = separator < 0 ? argument : argument.slice(0, separator)
    if (option === '--parallelism' || option === '--parellelism')
      throw new Error('Use --workers to select the number of isolated E2E shards.')
    if (option === '--fully-parallel')
      throw new Error('The E2E shared fixtures require serial tests inside each isolated shard. Use --workers instead of --fully-parallel.')
    if (SERIAL_OPTIONS.has(option))
      serial = true
    const takesValue = VALUE_OPTIONS.has(option)
    if (!takesValue) {
      playwrightArgs.push(argument)
      continue
    }
    const value = separator < 0 ? input[++index] : argument.slice(separator + 1)
    if (value === undefined || value.includes('\0'))
      throw new Error(`The E2E ${option} option requires a value without NUL.`)
    if (option === '--workers' || option === '-j') {
      if (workerOption)
        throw new Error('The E2E worker count must appear only once.')
      workerOption = true
      workers = workerCount(value, capacity)
    }
    else {
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
      playwrightArgs.push(argument)
      if (separator < 0)
        playwrightArgs.push(value)
    }
  }
  return {
    workers: serial ? 1 : workers,
    playwrightArgs,
    ...(reporters === undefined ? {} : { reporters }),
    ...(outputDir === undefined ? {} : { outputDir }),
    serial: serial || workers === 1,
  }
}

/** Remove actual options while preserving values that resemble those options. */
function withoutRunOptions(args: readonly string[], removed: ReadonlySet<string>): string[] {
  const result: string[] = []
  for (let index = 0; index < args.length; index++) {
    const argument = args[index]!
    if (argument === '--') {
      result.push(...args.slice(index))
      break
    }
    const option = argument.split('=', 1)[0]
    if (option !== undefined && removed.has(option)) {
      if (!argument.includes('='))
        index++
      continue
    }
    result.push(argument)
    if (option !== undefined && VALUE_OPTIONS.has(option) && !argument.includes('='))
      result.push(args[++index]!)
  }
  return result
}

/** Move output and reporters to the parent while preserving every test filter. */
export function shardSelectionArgs(args: readonly string[]): string[] {
  return withoutRunOptions(args, new Set(['--reporter', '--output']))
}

/** Keep serial test artifacts inside this run without changing reporters or test filters. */
export function serialRunArgs(args: readonly string[], outputDir: string): string[] {
  return [`--output=${outputDir}`, ...withoutRunOptions(args, new Set(['--output']))]
}
