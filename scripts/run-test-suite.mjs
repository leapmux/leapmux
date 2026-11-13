import { spawn } from 'node:child_process'
import { closeSync, mkdirSync, mkdtempSync, openSync, writeFileSync } from 'node:fs'
import { constants } from 'node:os'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { Writable } from 'node:stream'
import { pipeline } from 'node:stream/promises'
import { pathToFileURL } from 'node:url'

const checkout = resolve(import.meta.dirname, '..')

/** Keep complete test output on disk while forwarding it with stream backpressure. */
export async function runTestSuite(label, command, args, {
  root = checkout,
  cwd = root,
  env = process.env,
  stdout = process.stdout,
  stderr = process.stderr,
} = {}) {
  if (typeof label !== 'string' || !/^[a-z0-9][a-z0-9-]{0,63}$/.test(label))
    throw new Error('The test suite label must start with a lowercase letter or digit. Use at most 64 lowercase letters, digits, or hyphens.')
  if (typeof command !== 'string' || !command || command.includes('\0') || !Array.isArray(args) || args.some(arg => typeof arg !== 'string' || arg.includes('\0')))
    throw new Error('The test command and arguments must be strings without NUL.')
  const parent = resolve(root, '.tmp/test-results')
  mkdirSync(parent, { recursive: true })
  const directory = mkdtempSync(join(parent, `${label}-`))
  const resultPath = join(directory, 'result.json')
  const record = {
    label,
    command,
    args,
    status: 'running',
    started: new Date().toISOString(),
  }
  writeFileSync(resultPath, JSON.stringify(record, null, 2))
  const log = openSync(join(directory, 'console.log'), 'wx')
  const errors = []
  const recordFailure = (error) => {
    if (!errors.includes(error))
      errors.push(error)
  }
  const destinations = [
    ...new Set([stdout, stderr]),
  ]
  for (const destination of destinations)
    destination.on('error', recordFailure)
  const started = performance.now()
  let code = 1
  let signal = null
  try {
    // Keep the child in the CI runner's process tree so runner cancellation also reaches the tests.
    const child = spawn(command, args, {
      cwd,
      env: { ...env, LEAPMUX_TEST_RESULTS_DIR: directory },
      stdio: ['inherit', 'pipe', 'pipe'],
    })
    const completion = new Promise((accept) => {
      child.once('error', recordFailure)
      child.once('close', (exitCode, exitSignal) => {
        signal = exitSignal
        code = exitCode !== null && exitCode >= 0
          ? exitCode
          : (exitSignal
              ? 128 + (constants.signals[exitSignal] ?? 1)
              : 1)
        accept()
      })
    })
    const capture = (source, destination) => pipeline(source, new Writable({
      write(chunk, _encoding, done) {
        try {
          writeFileSync(log, chunk)
          destination.write(chunk, done)
        }
        catch (error) {
          done(error)
        }
      },
    })).catch((error) => {
      recordFailure(error)
      if (child.exitCode === null && child.signalCode === null) {
        try {
          child.kill('SIGKILL')
        }
        catch (stopError) {
          recordFailure(stopError)
        }
      }
      child.stdout.destroy()
      child.stderr.destroy()
    })
    await Promise.all([
      completion,
      capture(child.stdout, stdout),
      capture(child.stderr, stderr),
    ])
  }
  catch (error) {
    recordFailure(error)
  }
  finally {
    // Writable callbacks precede their queued error events. Keep the handlers until those events run.
    await new Promise(accept => setImmediate(accept))
    for (const destination of destinations)
      destination.off('error', recordFailure)
    try {
      closeSync(log)
    }
    catch (error) {
      recordFailure(error)
    }
  }
  if (errors.length && code === 0)
    code = 1
  writeFileSync(resultPath, JSON.stringify({
    ...record,
    status: code === 0
      ? 'passed'
      : 'failed',
    exitCode: code,
    signal,
    durationMs: Math.round(performance.now() - started),
    errors: errors.map(String),
  }, null, 2))
  return code
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    process.exitCode = await runTestSuite(process.argv[2], process.argv[3], process.argv.slice(4), {
      root: process.cwd(),
    })
  }
  catch (error) {
    console.error(error)
    process.exitCode = 1
  }
}
