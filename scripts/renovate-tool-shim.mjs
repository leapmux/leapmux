#!/usr/bin/env bun
import { spawn, spawnSync } from 'node:child_process'
import { appendFileSync, existsSync, readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import process from 'node:process'
import { cratesRegistry, updateWithRepair } from './renovate-cargo-repair.mjs'
import { renovateModule, requireRenovate } from './renovate-engine.mjs'
import { activeExemptions } from './renovate-exemptions.mjs'
import { lockedVersions, readRevisionFile } from './renovate-lockfile-versions.mjs'

// Generated bun and cargo shims lead the PATH of Renovate's child processes and run this
// module under the real Bun. Each resolving call obeys the minimum release age:
// - bun: install without --frozen-lockfile, add, and update gain the age and exact excludes.
// - cargo: update runs unchanged, then repairCargoLock holds each new crate to the age.
//   When that repair fails after a full update, a selective update leaves the blocking
//   crates at their versions. A failed repair restores Cargo.lock and fails the call.
const semver = requireRenovate('semver')
const { api: npmVersioning } = await renovateModule('modules/versioning/npm/index.js')

const BUN_RESOLVING = new Set(['install', 'i', 'add', 'a', 'update'])
const BUN_VALUE_OPTIONS = new Set(['--cwd', '--config', '-c'])
const CARGO_VALUE_OPTIONS = new Set(['--config', '-Z', '-C', '--color'])
// The options of `cargo update` that take a value, and those that select the crates to update.
const CARGO_UPDATE_VALUE_OPTIONS = new Set([
  '--config',
  '-Z',
  '--color',
  '--manifest-path',
  '--lockfile-path',
])
const CARGO_UPDATE_SELECTORS = new Set([
  '-p',
  '--package',
  '-w',
  '--workspace',
  '--precise',
])
const AGE_OPTIONS = ['--minimum-release-age', '--minimum-release-age-excludes']

function optionValue(argv, name) {
  for (let index = 0; index < argv.length; index++) {
    if (argv[index] === name)
      return argv[index + 1] ?? null
    if (argv[index].startsWith(`${name}=`))
      return argv[index].slice(name.length + 1)
  }
  return null
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

function subcommand(argv, valueOptions) {
  return argv[subcommandIndex(argv, valueOptions)] ?? null
}

/** Return whether the options of `cargo update` update every crate, as lockfile maintenance does. */
export function updatesEveryCrate(options) {
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

/** Return whether a bun call resolves versions, so that the age applies to it. */
export function bunResolves(argv) {
  const command = subcommand(argv, BUN_VALUE_OPTIONS)
  return BUN_RESOLVING.has(command) && !((command === 'install' || command === 'i') && argv.includes('--frozen-lockfile'))
}

function exactDeclarations(packageJson) {
  const declared = new Set()
  for (const group of ['dependencies', 'devDependencies', 'optionalDependencies']) {
    for (const [name, requirement] of Object.entries(packageJson?.[group] ?? {})) {
      if (typeof requirement === 'string' && npmVersioning.isSingleVersion(requirement))
        declared.add(`${name}@${semver.clean(requirement.replace(/^=\s*/, ''))}`)
    }
  }
  return declared
}

function withoutAgeOptions(argv) {
  const kept = []
  for (let index = 0; index < argv.length; index++) {
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

/**
 * Plan one tool call. `project` holds what a resolving bun call reads from its project:
 * `packageJson` and `baseLock`, the bun.lock of HEAD or null.
 */
export function shimPlan(tool, argv, policy, project = {}) {
  if (tool === 'bun') {
    if (!bunResolves(argv))
      return { args: argv, repair: false }
    const base = new Set(project.baseLock == null
      ? []
      : [
          ...lockedVersions('bun', project.baseLock),
        ].flatMap(([name, versions]) => [...versions].map(version => `${name}@${version}`)))
    const declared = exactDeclarations(project.packageJson)
    const authorized = policy.authorized?.npm ?? {}
    // An authorized root is active only when the project newly declares its exact version.
    const activeRoots = Object.keys(authorized).filter(root => declared.has(root) && !base.has(root)).sort()
    const excludes = [
      ...new Set([
        ...base,
        ...activeExemptions(authorized, root => activeRoots.includes(root)),
      ]),
    ].sort()
    return {
      args: [
        ...withoutAgeOptions(argv),
        `--minimum-release-age=${policy.ageSeconds}`,
        ...(excludes.length > 0
          ? [`--minimum-release-age-excludes=${excludes.join(',')}`]
          : []),
      ],
      repair: false,
      activeRoots,
      baseExcludes: base.size,
    }
  }
  if (tool === 'cargo') {
    const toolchain = []
    while (toolchain.length < argv.length && argv[toolchain.length].startsWith('+'))
      toolchain.push(argv[toolchain.length])
    const rest = argv.slice(toolchain.length)
    const index = subcommandIndex(rest, CARGO_VALUE_OPTIONS)
    if (rest[index] !== 'update')
      return { args: argv, repair: false }
    const manifestArgs = []
    for (const option of ['--config', '--manifest-path']) {
      for (let index = 0; index < rest.length; index++) {
        if (rest[index] === option && index + 1 < rest.length)
          manifestArgs.push(option, rest[++index])
        else if (rest[index].startsWith(`${option}=`))
          manifestArgs.push(rest[index])
      }
    }
    const precise = optionValue(rest, '--precise')
    const spec = optionValue(rest, '--package') ?? optionValue(rest, '-p')
    return {
      args: argv,
      repair: true,
      selective: updatesEveryCrate(rest.slice(index + 1)),
      toolchain,
      manifestArgs,
      preciseTarget: precise && spec
        ? `${packageName(spec)}@${precise}`
        : null,
    }
  }
  throw new Error(`Unknown shimmed tool: ${tool}`)
}

function git(args, { cwd }) {
  return spawnSync('git', args, { cwd, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 })
}

function bunProject(argv) {
  const directory = resolve(optionValue(argv, '--cwd') ?? '.')
  const manifest = join(directory, 'package.json')
  const inRepository = git([
    'rev-parse',
    '--verify',
    '--quiet',
    'HEAD^{commit}',
  ], { cwd: directory }).status === 0
  return {
    packageJson: existsSync(manifest)
      ? JSON.parse(readFileSync(manifest, 'utf8'))
      : null,
    // Outside a repository, no version counts as locked by the base.
    baseLock: inRepository
      ? readRevisionFile('HEAD', './bun.lock', { cwd: directory, git })
      : null,
  }
}

function displayDirectory() {
  const top = git(['rev-parse', '--show-toplevel'], {
    cwd: process.cwd(),
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

function runInherited(command, args) {
  return new Promise((settle, reject) => {
    const child = spawn(command, args, { stdio: 'inherit' })
    const forward = signal => () => child.kill(signal)
    const handlers = Object.fromEntries(['SIGINT', 'SIGTERM', 'SIGHUP'].map(signal => [
      signal,
      forward(signal),
    ]))
    for (const [signal, handler] of Object.entries(handlers))
      process.on(signal, handler)
    const release = () => {
      for (const [signal, handler] of Object.entries(handlers))
        process.removeListener(signal, handler)
    }
    child.once('error', (error) => {
      release()
      reject(error)
    })
    child.once('close', (code, signal) => {
      release()
      settle({ code, signal })
    })
  })
}

function captureCargo(command, toolchain) {
  return (args) => {
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

if (import.meta.main) {
  const [tool, policyPath, ...argv] = process.argv.slice(2)
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
    const plan = shimPlan(tool, argv, policy, tool === 'bun' && bunResolves(argv)
      ? bunProject(argv)
      : {})
    Object.assign(record, {
      effectiveArgs: summarize(plan.args),
      activeRoots: plan.activeRoots ?? [],
      baseExcludes: plan.baseExcludes ?? 0,
      repairs: [],
    })
    const execute = async () => {
      const result = await runInherited(policy.realTools[tool], plan.args)
      exitCode = result.code
      signal = result.signal
      return result
    }
    if (plan.repair) {
      const outcome = await updateWithRepair({
        run: captureCargo(policy.realTools.cargo, plan.toolchain),
        update: execute,
        git,
        registry: cratesRegistry({
          api: policy.cratesApi,
          cacheDirectory: join(dirname(policyPath), 'crates'),
        }),
        policy,
        manifestArgs: plan.manifestArgs,
        preciseTarget: plan.preciseTarget,
        selective: plan.selective,
      })
      record.repairs = outcome.repairs
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
  record.exitCode = exitCode
  if (signal)
    record.signal = signal
  appendFileSync(policy.invocationLog, `${JSON.stringify(record)}\n`)
  if (signal)
    process.kill(process.pid, signal)
  else
    process.exit(exitCode ?? 1)
}
