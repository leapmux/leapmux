#!/usr/bin/env bun
// ci-resource-monitor.mjs -- Sample the resources of a CI runner while the job
// runs.
//
// Usage:
//   bun scripts/ci-resource-monitor.mjs start   # create the check run, detach the sampler
//   bun scripts/ci-resource-monitor.mjs run     # sample until stop asks to end (start runs this)
//   bun scripts/ci-resource-monitor.mjs stop    # end the sampler, complete the check run, print the log
//
// The ci.yaml jobs run it when the repository variable CI_RESOURCE_MONITOR is
// true.
//
// GitHub uploads no log for a job whose runner it lost, so the sampler
// publishes its samples to a check run of its own. That output stays readable
// after GitHub declares the runner lost, and as long as the machine can still
// reach GitHub, it shows what happened to the runner afterwards.
//
// Each OS has a probe:
//
// - Windows calls the Win32 API through bun:ffi, inside the sampler's own
//   process. When Windows can no longer start a process, the sampler keeps
//   sampling, and its canary process records when process creation failed.
// - Linux reads /proc, also inside the sampler's own process.
// - macOS has no /proc, so it runs ps, sysctl, vm_stat and netstat.

import { spawn, spawnSync } from 'node:child_process'
import { once } from 'node:events'
import { appendFileSync, existsSync, mkdirSync, readFileSync, rmSync, statfsSync, statSync, writeFileSync } from 'node:fs'
import { hostname } from 'node:os'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { linuxProbe, parseProcStat } from './ci-resource-monitor/linux.mjs'
import { macosProbe } from './ci-resource-monitor/macos.mjs'
import { formatGigabytes, formatMegabytes } from './ci-resource-monitor/metrics.mjs'
import { keepStandardHandlesFromChildren, windowsProbe } from './ci-resource-monitor/windows.mjs'

const SCRIPT = fileURLToPath(import.meta.url)

// A check run accepts at most 65535 characters in each output field.
export const MAX_OUTPUT_CHARACTERS = 60000
// Every sample whose index is a multiple of this count also records the
// process tables.
export const DETAIL_EVERY = 6
// stop waits this long for the sampler to publish its last sample and end.
const STOP_WAIT_MS = 60_000

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

/** Keeps the end of `text` within the size that a check run field accepts. */
export function limitText(text) {
  if (text.length <= MAX_OUTPUT_CHARACTERS)
    return text
  return `...${text.slice(text.length - MAX_OUTPUT_CHARACTERS)}`
}

/** Keeps only text that can contribute to a later check-run publication. */
export function appendTextTail(previous, text) {
  return `${previous}${text}`.slice(-MAX_OUTPUT_CHARACTERS)
}

/** Formats TCP connection counts by state, the most common state first. */
export function formatTcpStates(counts) {
  if (counts.size === 0)
    return 'none'
  return [...counts].sort((a, b) => b[1] - a[1]).map(([state, count]) => `${state}:${count}`).join(',')
}

export function utcTime(date = new Date()) {
  return date.toISOString().slice(11, 19)
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

function positiveSeconds(value, fallback) {
  const seconds = Number(value)
  return Number.isFinite(seconds) && seconds > 0
    ? seconds
    : fallback
}

/**
 * Reads the monitor's configuration from the environment of a GitHub Actions
 * step. MONITOR_SAMPLE_SECONDS and MONITOR_PUBLISH_SECONDS exist for tests.
 */
export function monitorConfig(env) {
  if (!env.RUNNER_TEMP)
    throw new Error('The resource monitor requires RUNNER_TEMP.')
  const directory = join(env.RUNNER_TEMP, 'leapmux-resource-monitor')
  return {
    directory,
    logPath: join(directory, 'monitor.log'),
    pidPath: join(directory, 'monitor.pid'),
    checkRunPath: join(directory, 'check-run-id'),
    stopPath: join(directory, 'stop'),
    endedPath: join(directory, 'ended'),
    completedPath: join(directory, 'check-completed'),
    sampleMs: positiveSeconds(env.MONITOR_SAMPLE_SECONDS, 10) * 1000,
    publishMs: positiveSeconds(env.MONITOR_PUBLISH_SECONDS, 30) * 1000,
    token: env.GH_TOKEN ?? '',
    repository: env.GITHUB_REPOSITORY ?? '',
    apiUrl: (env.GITHUB_API_URL || 'https://api.github.com').replace(/\/+$/, ''),
    checkName: `${env.RUNNER_OS || process.platform} ${env.RUNNER_ARCH || process.arch} resource monitor`,
    // A pull request's check run belongs to its head commit, not to the merge commit.
    headSha: env.MONITOR_HEAD_SHA || env.GITHUB_SHA || '',
    detailsUrl: `${env.GITHUB_SERVER_URL}/${env.GITHUB_REPOSITORY}/actions/runs/${env.GITHUB_RUN_ID}`,
    diskPaths: process.platform === 'win32'
      ? [`${env.SystemDrive || 'C:'}\\`, env.RUNNER_TEMP, env.GITHUB_WORKSPACE]
      : ['/', env.RUNNER_TEMP, env.GITHUB_WORKSPACE],
    image: `${env.ImageOS ?? 'unknown'} ${env.ImageVersion ?? ''}`.trim(),
  }
}

// ---------------------------------------------------------------------------
// GitHub
// ---------------------------------------------------------------------------

/**
 * Sends one REST request. It returns null when the job supplied no token, so
 * a run without a token (a fork) still keeps the local log.
 */
async function callGitHub(config, method, path, body) {
  if (!config.token || !config.repository)
    return null
  const response = await fetch(`${config.apiUrl}/repos/${config.repository}${path}`, {
    method,
    headers: {
      'Authorization': `Bearer ${config.token}`,
      'Accept': 'application/vnd.github+json',
      'Content-Type': 'application/json',
      'X-GitHub-Api-Version': '2022-11-28',
    },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(20_000),
  })
  if (!response.ok)
    throw new Error(`GitHub answered ${response.status} to ${method} ${path}: ${(await response.text()).slice(0, 200)}`)
  return response.json()
}

function publishOutput(config, checkRunId, { title, summary, text, complete }) {
  const body = {
    output: {
      title,
      summary: limitText(summary),
      text: limitText(text),
    },
  }
  if (complete) {
    body.status = 'completed'
    body.conclusion = 'neutral'
  }
  return callGitHub(config, 'PATCH', `/check-runs/${checkRunId}`, body)
}

// ---------------------------------------------------------------------------
// Probes
// ---------------------------------------------------------------------------

/** Builds the probe of `platform`, the value of process.platform. */
export async function createProbe(platform) {
  switch (platform) {
    case 'win32': return windowsProbe()
    case 'linux': return linuxProbe()
    case 'darwin': return macosProbe()
    default: throw new Error(`The resource monitor has no probe for ${platform}.`)
  }
}

// ---------------------------------------------------------------------------
// Samples
// ---------------------------------------------------------------------------

/** Starts one trivial process and reports how long it took, or how it failed. */
export function runCanary([command, args]) {
  const started = performance.now()
  const result = spawnSync(command, args, { stdio: 'ignore', timeout: 15_000, windowsHide: true })
  const elapsed = Math.round(performance.now() - started)
  if (result.error) {
    return result.error.code === 'ETIMEDOUT'
      ? 'hung'
      : `fail(${result.error.message})`
  }
  if (result.status === 0)
    return `ok(${elapsed}ms)`
  if (result.status === null)
    return `signal=${result.signal}(${elapsed}ms)`
  return `exit=0x${(result.status >>> 0).toString(16).toUpperCase().padStart(8, '0')}(${elapsed}ms)`
}

/** Reports the free space of each file system that holds one of `paths`. */
export function freeDisk(paths) {
  const parts = new Map()
  for (const path of paths) {
    if (!path)
      continue
    try {
      // statSync also rejects a missing path, on every OS.
      const { dev } = statSync(path)
      const key = process.platform === 'win32'
        ? resolve(path).slice(0, 2).toUpperCase()
        : String(dev)
      if (parts.has(key))
        continue
      const stats = statfsSync(path)
      // A drive key such as C: already ends with the separator.
      const label = process.platform === 'win32'
        ? key
        : `${path}:`
      parts.set(key, `${label}${formatGigabytes(Number(stats.bavail) * Number(stats.bsize))}`)
    }
    catch (error) {
      parts.set(path, `${path}:fail(${error.code ?? error.message})`)
    }
  }
  return [
    ...parts.values(),
  ].join(',')
}

/**
 * Composes one sample line, and on request the process tables. It takes every
 * reading as input, so a test can state each one.
 *
 * Windows requires creation times before displaying a parent's process name.
 * Unix probes use live parent relationships without creation times.
 * Windows keeps the parent PID of a process whose parent ended and gives that
 * PID to a new process, so the PID alone can identify an unrelated process.
 */
export function composeSample({ time, system, rows, counted, tcp, free, canary, memoryLabel, detail, extraDetail, parentStartTimesRequired = false }) {
  const byPid = new Map((rows ?? []).map(row => [row.pid, row]))
  const parentName = (row) => {
    const parent = byPid.get(row.parent)
    if (!parent || (parentStartTimesRequired && !(row.start > 0 && parent.start > 0)) || (row.start > 0 && parent.start > 0 && row.start < parent.start))
      return `pid${row.parent}`
    return parent.name
  }
  const count = name => rows === null
    ? 'unavailable'
    : String(rows.filter(row => row.name === name).length)
  const tests = rows === null
    ? 'unavailable'
    : [
        ...new Set(rows.filter(row => row.name.endsWith('.test')).map(row => row.name.slice(0, -5))),
      ].sort().join(',')
  const runner = rows === null
    ? 'unavailable'
    : rows.filter(row => row.name.startsWith('Runner.')).map(row => `${row.name.slice(7)}:${formatMegabytes(row.memory)}`).join(',') || 'absent'
  const fields = [
    ...system,
    ...counted.map(name => [
      name,
      count(name),
    ]),
    ['tcp', tcp],
    ['free', free],
    ['canary', canary],
    ['runner', runner],
    ['tests', tests],
  ]
  const line = [
    time,
    ...fields.map(([key, value]) => `${key}=${value}`),
  ].join(' ')
  if (!detail)
    return { line, detail: null }
  if (rows === null)
    return { line, detail: 'Process table unavailable.' }
  const byMemory = [...rows].sort((a, b) => b.memory - a.memory).slice(0, 12).map(row => [
    `  ${row.name.padEnd(28)}`,
    `pid=${String(row.pid).padEnd(6)}`,
    `${memoryLabel}=${formatMegabytes(row.memory).padEnd(6)}`,
    ...row.extra.map(([key, value]) => `${key}=${String(value).padEnd(6)}`),
    `parent=${parentName(row)}`,
  ].join(' '))
  const groups = new Map()
  for (const row of rows) {
    const members = groups.get(row.name)
    if (members)
      members.push(row)
    else
      groups.set(row.name, [row])
  }
  const byCount = [...groups].sort((a, b) => b[1].length - a[1].length).slice(0, 10).map(([name, members]) => {
    const parents = new Map()
    for (const member of members) parents.set(parentName(member), (parents.get(parentName(member)) ?? 0) + 1)
    const top = [...parents].sort((a, b) => b[1] - a[1]).slice(0, 4).map(([parent, total]) => `${parent}x${total}`).join(',')
    return `  ${name.padEnd(28)} x${String(members.length).padEnd(4)} parents=${top}`
  })
  return {
    line,
    detail: [
      `By ${memoryLabel} memory:`,
      ...byMemory,
      'By count:',
      ...byCount,
      ...(extraDetail
        ? extraDetail(rows, parentName)
        : []),
    ].join('\n'),
  }
}

export function takeSample(probe, config, index) {
  const collect = (read) => {
    try {
      return {
        value: read(),
      }
    }
    catch (error) {
      return { error: `fail(${error?.message ?? error})` }
    }
  }
  const processes = collect(() => probe.processes())
  const rows = processes.value ?? null
  const system = collect(() => probe.system(rows))
  const tcp = collect(() => formatTcpStates(probe.tcpStates()))
  const free = collect(() => freeDisk(config.diskPaths))
  const canary = collect(() => runCanary(probe.canary))
  return composeSample({
    time: utcTime(),
    system: [
      ...(system.value ?? [
        ['system', system.error],
      ]),
      ...(processes.error
        ? [
            ['process-table', processes.error],
          ]
        : []),
    ],
    rows,
    counted: probe.counted,
    tcp: tcp.error ?? tcp.value,
    free: free.error ?? free.value,
    canary: canary.error ?? canary.value,
    memoryLabel: probe.memoryLabel,
    detail: index % DETAIL_EVERY === 0,
    extraDetail: probe.extraDetail,
    parentStartTimesRequired: probe.parentStartTimesRequired,
  })
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

function appendLog(config, text) {
  appendFileSync(config.logPath, `${text}\n`)
}

function readCheckRunId(config) {
  if (!existsSync(config.checkRunPath))
    return null
  return readFileSync(config.checkRunPath, 'utf8').trim() || null
}

function warn(message) {
  process.stdout.write(`::warning::${message}\n`)
}

async function start(config) {
  mkdirSync(config.directory, { recursive: true })
  for (const path of [
    config.stopPath,
    config.endedPath,
    config.completedPath,
    config.checkRunPath,
    config.pidPath,
    config.logPath,
  ])
    rmSync(path, { force: true })
  try {
    const created = await callGitHub(config, 'POST', '/check-runs', {
      name: config.checkName,
      head_sha: config.headSha,
      status: 'in_progress',
      details_url: config.detailsUrl,
      output: { title: 'Starting', summary: 'The monitor publishes its first sample shortly.' },
    })
    if (created) {
      writeFileSync(config.checkRunPath, String(created.id))
      process.stdout.write(`The resource monitor publishes to check run ${created.id}: ${created.html_url}\n`)
    }
    else {
      process.stdout.write('The resource monitor has no token, so it keeps a local log only.\n')
    }
  }
  catch (error) {
    warn(`The resource monitor could not create its check run, so it keeps a local log only: ${error.message}`)
  }
  await keepStandardHandlesFromChildren()
  const sampler = spawn(process.execPath, [SCRIPT, 'run'], { detached: true, stdio: 'ignore', windowsHide: true })
  await once(sampler, 'spawn')
  sampler.unref()
  writeFileSync(config.pidPath, String(sampler.pid))
  process.stdout.write(`Started the resource monitor as process ${sampler.pid}. Its log is ${config.logPath}.\n`)
}

const sleep = ms => new Promise(resolveSleep => setTimeout(resolveSleep, ms))

export async function run(config, probeFactory = createProbe) {
  try {
    const probe = await probeFactory(process.platform)
    const header = [
      ...probe.header(),
      `image=${config.image} machine=${hostname()}`,
    ].join('\n')
    appendLog(config, header)
    const checkRunId = readCheckRunId(config)
    let lastLine = ''
    let publicationText = ''
    let lastDetail = ''
    let lastPublish = 0
    let publish = 'pending'
    for (let index = 0; ; index++) {
      const stopping = existsSync(config.stopPath)
      try {
        const sample = takeSample(probe, config, index)
        const line = `${sample.line} pub=${publish}`
        appendLog(config, line)
        lastLine = line
        if (sample.detail) {
          appendLog(config, sample.detail)
          lastDetail = sample.detail
        }
      }
      catch (error) {
        const line = `${utcTime()} sample failed: ${error.message}`
        appendLog(config, line)
        lastLine = line
      }
      if (checkRunId)
        publicationText = appendTextTail(publicationText, `${lastLine}\n`)
      if (checkRunId && (stopping || Date.now() - lastPublish >= config.publishMs)) {
        const started = performance.now()
        try {
          const fence = '```'
          const published = await publishOutput(config, checkRunId, {
            title: `Sample ${index + 1} at ${utcTime()} UTC`,
            summary: `${fence}\n${header}\n\n${lastLine}\n${lastDetail}\n${fence}`,
            text: `${fence}\n${publicationText}${fence}`,
            complete: stopping,
          })
          if (stopping && published)
            writeFileSync(config.completedPath, '')
          publish = `ok(${Math.round(performance.now() - started)}ms)`
        }
        catch (error) {
          publish = `fail(${Math.round(performance.now() - started)}ms: ${error.message})`
        }
        lastPublish = Date.now()
      }
      if (stopping) {
        appendLog(config, `The monitor ended after ${index + 1} samples. Last publish: ${publish}`)
        return
      }
      await sleep(config.sampleMs)
    }
  }
  catch (error) {
    appendLog(config, `The monitor failed: ${error.message}`)
    throw error
  }
  finally {
    writeFileSync(config.endedPath, '')
  }
}

/**
 * Reports whether a process with `pid` runs. On Linux a process that ended
 * stays a zombie until its parent reaps it, and the zombie still answers a
 * signal, so the state in /proc decides.
 */
export function isRunning(pid) {
  try {
    process.kill(pid, 0)
  }
  catch (error) {
    return error.code === 'EPERM'
  }
  if (process.platform !== 'linux')
    return true
  try {
    const stat = parseProcStat(readFileSync(`/proc/${pid}/stat`, 'utf8'))
    return stat !== null && !/^[ZX]$/.test(stat.state)
  }
  catch {
    return false
  }
}

/**
 * Asks the sampler to end and waits for its ended marker. It never kills the
 * sampler by PID: the PID can belong to an unrelated process by then, and the
 * runner ends a leftover process when the job ends.
 */
async function stop(config) {
  if (!existsSync(config.directory)) {
    process.stdout.write('The resource monitor did not start.\n')
    return
  }
  writeFileSync(config.stopPath, '')
  const pid = existsSync(config.pidPath)
    ? Number(readFileSync(config.pidPath, 'utf8'))
    : 0
  const deadline = Date.now() + STOP_WAIT_MS
  // A sampler that no longer runs and left no marker crashed, so the wait
  // ends at once. A PID that a new process took only makes the wait longer.
  const samplerRuns = () => pid > 0 && isRunning(pid)
  while (!existsSync(config.endedPath) && Date.now() < deadline && samplerRuns())
    await sleep(250)
  const ended = existsSync(config.endedPath)
  const log = existsSync(config.logPath)
    ? readFileSync(config.logPath, 'utf8')
    : ''
  const checkRunId = readCheckRunId(config)
  if (!ended)
    warn(`The resource monitor ${pid} did not end within ${STOP_WAIT_MS / 1000} seconds of the request.`)
  // A sampler can finish after initialization or final publication failed.
  // Only an acknowledged completion suppresses the fallback request.
  if (checkRunId && !existsSync(config.completedPath)) {
    if (ended)
      warn('The resource monitor ended without completing its check run.')
    try {
      const fence = '```'
      const published = await publishOutput(config, checkRunId, {
        title: ended ? 'The monitor could not complete its check run' : 'The monitor ended early',
        summary: 'The monitor did not acknowledge check completion. The text holds the end of its log.',
        text: `${fence}\n${log.split('\n').slice(-200).join('\n')}\n${fence}`,
        complete: true,
      })
      if (published)
        writeFileSync(config.completedPath, '')
    }
    catch (error) {
      warn(`The resource monitor could not complete its check run: ${error.message}`)
    }
  }
  process.stdout.write(`::group::Resource monitor log\n${log || 'The resource monitor wrote no log.\n'}::endgroup::\n`)
}

/**
 * Runs one mode. start and stop report a failure as a warning and succeed,
 * because diagnostics must never fail the job.
 */
export async function main(argv, env) {
  const mode = argv[0]
  if (!['start', 'run', 'stop'].includes(mode)) {
    process.stderr.write('usage: bun scripts/ci-resource-monitor.mjs start|run|stop\n')
    return 2
  }
  let config
  try {
    config = monitorConfig(env)
  }
  catch (error) {
    warn(error.message)
    return mode === 'run'
      ? 1
      : 0
  }
  if (mode === 'run') {
    await run(config)
    return 0
  }
  try {
    await (mode === 'start'
      ? start(config)
      : stop(config))
  }
  catch (error) {
    warn(`The resource monitor did not ${mode} cleanly: ${error.message}`)
  }
  return 0
}

if (import.meta.main)
  process.exit(await main(process.argv.slice(2), process.env))
