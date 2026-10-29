#!/usr/bin/env node
import { spawn } from 'node:child_process'
import { appendFileSync, writeFileSync, writeSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'

const nativeRuntime = {
  spawn,
  kill: process.kill,
  setTimeout,
  clearTimeout,
}

function reportFailure(message) {
  try {
    writeSync(2, `${message}\n`)
  }
  catch { /* A closed diagnostic stream must not interrupt process termination. */ }
}

export function runWithTimeout(command, args, { timeoutMs = 50 * 60 * 1000, env = process.env, cwd = process.cwd(), stdio = 'inherit', output, runtime = nativeRuntime, streams = { stdout: process.stdout, stderr: process.stderr } } = {}) {
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0 || timeoutMs > 2147483647)
    return Promise.reject(new Error('The execution timeout must be an integer from 1 through 2147483647 milliseconds.'))
  return new Promise((resolveResult, reject) => {
    const started = performance.now()
    const child = runtime.spawn(command, args, {
      env,
      cwd,
      stdio,
      detached: process.platform !== 'win32',
    })
    const pid = child.pid
    let timedOut = false
    let finished = false
    let timer
    let pendingWrites = 0
    let closeResult
    const destinations = output
      ? [
          ...new Set([streams.stdout, streams.stderr]),
        ]
      : []
    const drain = () => {
      if (pendingWrites)
        return
      // Writable callbacks run before their queued error event. Keep handlers through that event.
      setImmediate(() => {
        if (pendingWrites)
          return
        if (!finished && closeResult)
          complete(null, ...closeResult)
        if (finished) {
          for (const destination of destinations)
            destination.removeListener('error', fail)
        }
      })
    }
    const stop = () => {
      if (!pid)
        return
      if (process.platform === 'win32') {
        if (child.exitCode !== null || child.signalCode !== null)
          return
        const killer = runtime.spawn('taskkill', [
          '/PID',
          String(pid),
          '/T',
          '/F',
        ], { stdio: 'ignore' })
        killer.once('error', (error) => {
          if (child.exitCode === null && child.signalCode === null)
            child.kill('SIGKILL')
          reportFailure(`Failed to terminate the Renovate process tree: ${error.message}`)
        })
        killer.unref()
      }
      else {
        try {
          // Descendants can retain inherited output pipes after the group leader exits.
          runtime.kill(-pid, 'SIGKILL')
        }
        catch (error) {
          if (error.code === 'ESRCH')
            return
          if (error.code === 'EPERM') {
            // A process can exit between stream delivery and process-group termination.
            // The child handle can still terminate a live process when its group is unavailable.
            if (child.exitCode === null && child.signalCode === null)
              child.kill('SIGKILL')
            return
          }
          throw error
        }
      }
    }
    const interrupted = (signal) => {
      if (finished)
        return
      timedOut = !signal
      complete(null, signal === 'SIGINT'
        ? 130
        : signal === 'SIGTERM'
          ? 143
          : 124, signal ?? child.signalCode)
      try {
        stop()
      }
      catch (error) { reportFailure(`Failed to terminate Renovate: ${error.message}`) }
    }
    const terminated = () => interrupted('SIGTERM')
    const cancelled = () => interrupted('SIGINT')
    process.once('SIGTERM', terminated)
    process.once('SIGINT', cancelled)
    const cleanup = () => {
      runtime.clearTimeout(timer)
      process.removeListener('SIGTERM', terminated)
      process.removeListener('SIGINT', cancelled)
    }
    function complete(error, code, signal) {
      if (finished)
        return
      finished = true
      cleanup()
      // An escaped descendant can retain these pipes after the leader or process group exits.
      child.stdin?.destroy()
      child.stdout?.removeListener('data', forwardStdout)
      child.stderr?.removeListener('data', forwardStderr)
      child.stdout?.destroy()
      child.stderr?.destroy()
      child.unref()
      drain()
      if (error) {
        reject(error)
      }
      else {
        resolveResult({
          code: timedOut
            ? 124
            : code ?? 1,
          signal,
          timedOut,
          durationMs: Math.round(performance.now() - started),
        })
      }
    }
    function fail(error) {
      if (finished)
        return
      complete(error)
      try {
        stop()
      }
      catch (stopError) {
        reportFailure(`Failed to terminate Renovate: ${stopError.message}`)
      }
    }
    const forward = (data, stream) => {
      if (finished)
        return
      let pending = false
      const written = (error) => {
        if (!pending)
          return
        pending = false
        pendingWrites--
        if (error)
          fail(error)
        drain()
      }
      try {
        output.write(data)
        pending = true
        pendingWrites++
        stream.write(data, written)
      }
      catch (error) {
        written(error)
        fail(error)
      }
    }
    function forwardStdout(data) {
      forward(data, streams.stdout)
    }
    function forwardStderr(data) {
      forward(data, streams.stderr)
    }
    timer = runtime.setTimeout(interrupted, timeoutMs)
    if (output) {
      for (const destination of destinations)
        destination.on('error', fail)
      child.stdout.on('data', forwardStdout)
      child.stderr.on('data', forwardStderr)
      for (const source of [child.stdout, child.stderr]) {
        source.on('error', fail)
        source.once('close', () => source.removeListener('error', fail))
      }
    }
    child.once('error', fail)
    child.once('close', (code, signal) => {
      if (!output) {
        complete(null, code, signal)
      }
      else {
        closeResult = [code, signal]
        drain()
      }
    })
  })
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const root = resolve(import.meta.dirname, '..')
    writeFileSync(resolve(root, '.tmp/renovate-pr-details.jsonl'), '')
    const output = {
      write: data => appendFileSync(resolve(root, '.tmp/renovate-cli.log'), data),
    }
    const result = await runWithTimeout(process.execPath, [
      resolve(root, '.github/renovate/node_modules/renovate/dist/renovate.js'),
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      output,
    })
    appendFileSync(resolve(root, '.tmp/renovate-execution.jsonl'), `${JSON.stringify(result)}\n`)
    if (result.timedOut)
      reportFailure('Renovate exceeded its 50-minute execution limit.')
    process.exitCode = result.code
  }
  catch (error) {
    reportFailure(error.message)
    process.exitCode = 1
  }
}
