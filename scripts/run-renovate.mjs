#!/usr/bin/env node
import { spawn } from 'node:child_process'
import { appendFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'

export function runWithTimeout(command, args, { timeoutMs = 50 * 60 * 1000, env = process.env, stdio = 'inherit', output } = {}) {
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0 || timeoutMs > 2147483647)
    return Promise.reject(new Error('The execution timeout must be an integer from 1 through 2147483647 milliseconds.'))
  return new Promise((resolveResult, reject) => {
    const started = Date.now()
    const child = spawn(command, args, { env, stdio, detached: process.platform !== 'win32' })
    let timedOut = false
    let timer
    const stop = () => {
      if (!child.pid || child.exitCode !== null || child.signalCode !== null)
        return
      if (process.platform === 'win32') {
        spawn('taskkill', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore' })
      }
      else {
        try {
          process.kill(-child.pid, 'SIGKILL')
        }
        catch (error) {
          if (error.code === 'ESRCH')
            return
          if (error.code === 'EPERM') {
            // A process can exit between stream delivery and process-group termination.
            // The child handle can still terminate a live process when its group is unavailable.
            child.kill('SIGKILL')
            return
          }
          throw error
        }
      }
    }
    const interrupted = () => {
      timedOut = true
      stop()
    }
    process.once('SIGTERM', interrupted)
    process.once('SIGINT', interrupted)
    const cleanup = () => {
      clearTimeout(timer)
      process.removeListener('SIGTERM', interrupted)
      process.removeListener('SIGINT', interrupted)
    }
    const fail = (error) => {
      cleanup()
      reject(error)
      try {
        stop()
      }
      catch (stopError) {
        process.stderr.write(`Failed to terminate Renovate: ${stopError.message}\n`)
      }
    }
    const forward = (data, stream) => {
      try {
        output.write(data)
        stream.write(data)
      }
      catch (error) {
        fail(error)
      }
    }
    timer = setTimeout(() => {
      timedOut = true
      try {
        stop()
      }
      catch (error) {
        fail(error)
      }
    }, timeoutMs)
    if (output) {
      child.stdout.on('data', data => forward(data, process.stdout))
      child.stderr.on('data', data => forward(data, process.stderr))
    }
    child.once('error', (error) => {
      cleanup()
      reject(error)
    })
    child.once('close', (code, signal) => {
      cleanup()
      resolveResult({ code: timedOut ? 124 : code ?? 1, signal, timedOut, durationMs: Date.now() - started })
    })
  })
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const root = resolve(import.meta.dirname, '..')
    const output = { write: data => appendFileSync(resolve(root, '.tmp/renovate-cli.log'), data) }
    const result = await runWithTimeout(process.execPath, [resolve(root, '.github/renovate/node_modules/renovate/dist/renovate.js')], { stdio: ['ignore', 'pipe', 'pipe'], output })
    appendFileSync(resolve(root, '.tmp/renovate-execution.jsonl'), `${JSON.stringify(result)}\n`)
    if (result.timedOut)
      console.error('Renovate exceeded its 50-minute execution limit.')
    process.exitCode = result.code
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
