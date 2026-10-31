#!/usr/bin/env bun
import { spawn, spawnSync } from 'node:child_process'
import { appendFileSync, existsSync, readFileSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import process from 'node:process'

// Generated bun and cargo shims lead the PATH of Renovate's child processes and run this
// module under the real Bun. These calls obey the minimum release age:
// - bun: install without --frozen-lockfile, add, update, remove, link, and patch gain the
//   age and exact excludes.
// - cargo: update resolves through the aged crate index, which serves no young version,
//   and a check after it holds each new crate to the age. When that check fails after a
//   full update, a selective update leaves the blocking crates at their versions. A
//   failed check restores Cargo.lock and fails the call.
// Every other call passes through unchanged, including a cargo subcommand that can also
// write Cargo.lock, such as generate-lockfile or add. Required CI checks every lockfile
// version that a Renovate branch adds, whichever call wrote it.
// This module loads Renovate's code only for a call above, because every bun call of a
// post-upgrade task, such as each `bun scripts/task-state.mjs`, passes through it.

const BUN_RESOLVING = new Set([
  'install',
  'i',
  'add',
  'a',
  'update',
  'remove',
  'rm',
  'link',
  'patch',
])
const CARGO_VALUE_OPTIONS = new Set(['--config', '-Z', '-C', '--color'])
// The options of `cargo update` that take a value, and those that select the crates to update.
const CARGO_UPDATE_VALUE_OPTIONS = new Set([
  '--config',
  '-Z',
  '--color',
  '--manifest-path',
  '-m',
  '--lockfile-path',
])
const CARGO_UPDATE_SELECTORS = new Set([
  '-p',
  '--package',
  '-w',
  '--workspace',
  '--precise',
])
// The repair locates Cargo.lock from the manifest and the current directory only.
const CARGO_UNSUPPORTED_OPTIONS = ['-C', '--lockfile-path']
const AGE_OPTIONS = ['--minimum-release-age', '--minimum-release-age-excludes']

// Return the value of an option: `--name value`, `--name=value`, or, for a short option,
// `-nvalue` as well.
function optionValue(argv, name) {
  const short = /^-\w$/.test(name)
  for (let index = 0; index < argv.length; index++) {
    if (argv[index] === name)
      return argv[index + 1] ?? null
    if (argv[index].startsWith(`${name}=`))
      return argv[index].slice(name.length + 1)
    if (short && argv[index].startsWith(name) && argv[index].length > name.length)
      return argv[index].slice(name.length)
  }
  return null
}

function hasOption(argv, name) {
  return argv.some(arg => arg === name || arg.startsWith(`${name}=`) || (/^-\w$/.test(name) && arg.startsWith(name)))
}

function subcommandIndex(argv, valueOptions) {
  for (let index = 0; index < argv.length; index++) {
    const arg = argv[index]
    if (valueOptions.has(arg))
      index++
    else if (!arg.startsWith('-') && !arg.startsWith('+'))
      return index
  }
  return -1
}

/** Return whether the options of `cargo update` update every crate, as lockfile maintenance does. */
function updatesEveryCrate(options) {
  for (let index = 0; index < options.length; index++) {
    const arg = options[index]
    // A package, the workspace, a precise version, or a positional spec selects crates.
    if (CARGO_UPDATE_SELECTORS.has(arg.split('=')[0]) || /^-p./.test(arg) || !arg.startsWith('-'))
      return false
    if (CARGO_UPDATE_VALUE_OPTIONS.has(arg))
      index++
  }
  return true
}

/**
 * Return the index of the bun subcommand. Bun takes the first argument that is not an
 * option as its command, even after an option that takes a separate value:
 * `bun --cwd dir install` runs the script `dir`, not `install`.
 */
function bunCommandIndex(argv) {
  return argv.findIndex(arg => !arg.startsWith('-'))
}

/** Return whether a bun call resolves versions, so that the age applies to it. */
export function bunResolves(argv) {
  const index = bunCommandIndex(argv)
  const command = argv[index]
  if (!BUN_RESOLVING.has(command))
    return false
  return !((command === 'install' || command === 'i') && argv.some(arg => arg === '--frozen-lockfile' || (arg.startsWith('--frozen-lockfile=') && arg !== '--frozen-lockfile=false')))
}

// Remove the age options that the caller passes before `--`. An argument after `--` is not an option.
function withoutAgeOptions(argv) {
  const kept = []
  for (let index = 0; index < argv.length; index++) {
    if (argv[index] === '--') {
      kept.push(...argv.slice(index))
      break
    }
    if (AGE_OPTIONS.includes(argv[index]))
      index++
    else if (!AGE_OPTIONS.some(option => argv[index].startsWith(`${option}=`)))
      kept.push(argv[index])
  }
  return kept
}

function packageName(spec) {
  const identity = spec.slice(spec.lastIndexOf('#') + 1)
  return identity.split('@')[0]
}

async function bunPlan(argv, policy, project) {
  const { activeExemptions, exactIdentity } = await import('./renovate-exemptions.mjs')
  const { lockedVersions, versionIdentities } = await import('./renovate-lockfile-versions.mjs')
  const baseVersions = project.baseLock == null
    ? new Map()
    : lockedVersions('bun', project.baseLock)
  const base = versionIdentities(baseVersions)
  const declared = new Set()
  for (const group of ['dependencies', 'devDependencies', 'optionalDependencies']) {
    for (const [name, requirement] of Object.entries(project.packageJson?.[group] ?? {})) {
      const identity = exactIdentity('npm', name, requirement)
      if (identity)
        declared.add(identity)
    }
  }
  // An authorized root is active only when the project newly declares its exact version.
  const authorized = project.lockfile == null
    ? undefined
    : policy.authorized?.[project.lockfile]
  const { roots: activeRoots, exempt } = activeExemptions(authorized, {
    base: baseVersions,
    selected: root => declared.has(root),
  })
  const excludes = [
    ...new Set([
      ...base,
      ...exempt,
    ]),
  ].sort()
  const args = withoutAgeOptions(argv)
  // Options follow the subcommand, so that no `--` can turn them into package specs.
  const command = bunCommandIndex(args)
  return {
    args: [
      ...args.slice(0, command + 1),
      `--minimum-release-age=${policy.ageSeconds}`,
      ...(excludes.length > 0
        ? [`--minimum-release-age-excludes=${excludes.join(',')}`]
        : []),
      ...args.slice(command + 1),
    ],
    agedUpdate: false,
    activeRoots,
    baseExcludes: base.size,
  }
}

function cargoPlan(argv) {
  const toolchain = []
  while (toolchain.length < argv.length && argv[toolchain.length].startsWith('+'))
    toolchain.push(argv[toolchain.length])
  const rest = argv.slice(toolchain.length)
  const index = subcommandIndex(rest, CARGO_VALUE_OPTIONS)
  const options = rest.slice(index + 1)
  if (rest[index] !== 'update' || options.includes('--help') || options.includes('-h'))
    return { args: argv, agedUpdate: false }
  const unsupported = CARGO_UNSUPPORTED_OPTIONS.find(option => hasOption(rest, option))
  if (unsupported)
    throw new Error(`The release-age shim cannot hold cargo update to the age with ${unsupported}.`)
  const manifestArgs = []
  for (let position = 0; position < rest.length; position++) {
    const arg = rest[position]
    if ((arg === '--config' || arg === '--manifest-path' || arg === '-m') && position + 1 < rest.length) {
      manifestArgs.push(arg === '-m'
        ? '--manifest-path'
        : arg, rest[++position])
    }
    else if (arg.startsWith('--config=') || arg.startsWith('--manifest-path=')) {
      manifestArgs.push(arg)
    }
  }
  const precise = optionValue(rest, '--precise')
  const spec = optionValue(rest, '--package') ?? optionValue(rest, '-p')
  return {
    args: argv,
    agedUpdate: true,
    fullUpdate: updatesEveryCrate(options),
    toolchain,
    manifestArgs,
    preciseTarget: precise && spec
      ? `${packageName(spec)}@${precise}`
      : null,
  }
}

/**
 * Plan one tool call. `readProject()` returns what a resolving bun call reads from its
 * project: `packageJson` and `baseLock`, the bun.lock of HEAD or null. Only such a call
 * reads it.
 */
export async function shimPlan(tool, argv, policy, readProject = () => ({})) {
  if (tool === 'bun') {
    return bunResolves(argv)
      ? bunPlan(argv, policy, await readProject())
      : { args: argv, agedUpdate: false }
  }
  if (tool === 'cargo')
    return cargoPlan(argv)
  throw new Error(`Unknown shimmed tool: ${tool}`)
}

const FORWARDED_SIGNALS = Object.freeze(['SIGINT', 'SIGTERM', 'SIGHUP'])

/**
 * Track the signals that stop the shim for its whole run. A signal reaches the attached
 * child at once. A signal that arrives during a synchronous cargo call reaches its handler
 * only when the event loop turns, so a check right after that call cannot see it.
 * `deliverPending` waits for that turn. `assertRunning` stops the run at the first check
 * after the handler runs, and the repair then restores Cargo.lock. Until then, the cargo
 * calls of the repair continue.
 */
export function trackSignals(signals = FORWARDED_SIGNALS) {
  let pending = null
  let child = null
  const handlers = signals.map(signal => [
    signal,
    () => {
      pending ??= signal
      child?.kill(signal)
    },
  ])
  for (const [signal, handler] of handlers)
    process.on(signal, handler)
  return {
    get pending() {
      return pending
    },
    attach(next) {
      child = next
    },
    detach() {
      child = null
    },
    assertRunning() {
      if (pending)
        throw new Error(`The shim received ${pending}.`)
    },
    deliverPending() {
      return new Promise(resolve => setImmediate(resolve))
    },
    release() {
      for (const [signal, handler] of handlers)
        process.removeListener(signal, handler)
    },
  }
}

async function bunProject(argv) {
  const { readRevisionFile, runGit } = await import('./renovate-lockfile-versions.mjs')
  const directory = resolve(optionValue(argv, '--cwd') ?? '.')
  const manifest = join(directory, 'package.json')
  const inRepository = runGit([
    'rev-parse',
    '--verify',
    '--quiet',
    'HEAD^{commit}',
  ], { cwd: directory }).status === 0
  return {
    packageJson: existsSync(manifest)
      ? JSON.parse(readFileSync(manifest, 'utf8'))
      : null,
    // Outside a repository, no version counts as locked by the base, and no lockfile has
    // an authorized fix.
    baseLock: inRepository
      ? readRevisionFile('HEAD', './bun.lock', { cwd: directory })
      : null,
    lockfile: inRepository
      ? `${runGit(['rev-parse', '--show-prefix'], { cwd: directory }).stdout.trim()}bun.lock`
      : null,
  }
}

function displayDirectory() {
  const top = spawnSync('git', ['rev-parse', '--show-toplevel'], {
    cwd: process.cwd(),
    encoding: 'utf8',
  })
  return top.status === 0
    ? relative(top.stdout.trim(), process.cwd()) || '.'
    : process.cwd()
}

// The exclude list repeats the whole base lockfile. The record keeps its size only.
function summarize(args) {
  return args.map(arg => arg.startsWith('--minimum-release-age-excludes=')
    ? `--minimum-release-age-excludes=…(${arg.split('=')[1].split(',').length} entries)`
    : arg)
}

async function runInherited(command, args, signals) {
  // A signal during the planning, or during a synchronous cargo call before this one, stops
  // the call before the real tool starts.
  await signals.deliverPending()
  signals.assertRunning()
  return new Promise((settle, reject) => {
    const child = spawn(command, args, { stdio: 'inherit' })
    signals.attach(child)
    child.once('error', (error) => {
      signals.detach()
      reject(error)
    })
    child.once('close', (code, signal) => {
      signals.detach()
      settle({ code, signal })
    })
  })
}

function captureCargo(command, toolchain, signals) {
  return (args) => {
    signals.assertRunning()
    const result = spawnSync(command, [
      ...toolchain,
      ...args,
    ], { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 })
    // Keep cargo's own report in Renovate's log.
    if (result.stderr)
      process.stderr.write(result.stderr)
    return {
      status: result.error ? null : result.status,
      stdout: result.stdout ?? '',
      stderr: result.stderr || result.error?.message || '',
    }
  }
}

/**
 * Run one shimmed call: plan it, run the real tool, through the aged crate index and with
 * the check of Cargo.lock for a cargo update, and append the record to the invocation log.
 * Return the exit code and the signal that ended the call, which the caller raises. The
 * signal handlers stop before the return.
 */
export async function main([tool, policyPath, ...argv], { signals = trackSignals() } = {}) {
  try {
    const policy = JSON.parse(readFileSync(policyPath, 'utf8'))
    const record = {
      time: new Date().toISOString(),
      tool,
      cwd: displayDirectory(),
      args: argv,
    }
    let exitCode = 1
    let signal = null
    try {
      const plan = await shimPlan(tool, argv, policy, () => bunProject(argv))
      Object.assign(record, {
        effectiveArgs: summarize(plan.args),
        activeRoots: plan.activeRoots ?? [],
        baseExcludes: plan.baseExcludes ?? 0,
      })
      // Global cargo options, such as those of the aged index, follow the toolchain.
      const execute = async (cargoArgs = []) => {
        const toolchain = plan.toolchain ?? []
        const result = await runInherited(policy.realTools[tool], [
          ...toolchain,
          ...cargoArgs,
          ...plan.args.slice(toolchain.length),
        ], signals)
        exitCode = result.code
        signal = result.signal
        return result
      }
      if (plan.agedUpdate) {
        const [{ updateWithAge }, { runGit }] = await Promise.all([
          import('./renovate-cargo-age.mjs'),
          import('./renovate-lockfile-versions.mjs'),
        ])
        const outcome = await updateWithAge({
          run: captureCargo(policy.realTools.cargo, plan.toolchain, signals),
          update: execute,
          git: runGit,
          policy,
          manifestArgs: plan.manifestArgs,
          preciseTarget: plan.preciseTarget,
          fullUpdate: plan.fullUpdate,
        })
        record.activeRoots = outcome.activeRoots
        // A selective update records the crates that kept their versions.
        if (outcome.excluded)
          record.excluded = outcome.excluded
      }
      else {
        await execute()
      }
    }
    catch (error) {
      process.stderr.write(`${error.message}\n`)
      record.error = error.message
      exitCode = 1
    }
    // A signal from the last synchronous cargo call must still stop the shim.
    await signals.deliverPending()
    signal ??= signals.pending
    record.exitCode = exitCode
    if (signal)
      record.signal = signal
    appendFileSync(policy.invocationLog, `${JSON.stringify(record)}\n`)
    return { exitCode, signal, record }
  }
  finally {
    // The caller raises the signal itself, so the shim must no longer handle it.
    signals.release()
  }
}

if (import.meta.main) {
  try {
    const { exitCode, signal } = await main(process.argv.slice(2))
    // Stop with the signal itself, as the real tool would.
    if (signal)
      process.kill(process.pid, signal)
    else
      process.exit(exitCode ?? 1)
  }
  catch (error) {
    process.stderr.write(`${error.message}\n`)
    process.exit(1)
  }
}
