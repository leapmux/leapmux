import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'

/**
 * Run a command without blocking the event loop, so that an in-process server can answer
 * it, and collect its output. A spawn failure resolves with `error` and a null status.
 */
export function runCommand(command, args, options) {
  return new Promise((settle) => {
    let stdout = ''
    let stderr = ''
    const child = spawn(command, args, {
      ...options,
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    child.stdout.setEncoding('utf8').on('data', (data) => {
      stdout += data
    })
    child.stderr.setEncoding('utf8').on('data', (data) => {
      stderr += data
    })
    child.once('error', error => settle({ error, status: null, stdout, stderr }))
    child.once('close', status => settle({ status, stdout, stderr }))
  })
}

/**
 * Run `go` in a new scratch directory under `<checkout>/.tmp`, outside every workspace and
 * module, with the installed toolchain only. `files` maps a file name to the text that the
 * directory holds before the command runs. The directory is removed after the command ends.
 */
export async function runGoOutsideModules(args, { checkout, env = process.env, files = {}, run = runCommand }) {
  mkdirSync(resolve(checkout, '.tmp'), { recursive: true })
  const directory = mkdtempSync(resolve(checkout, '.tmp/go-'))
  try {
    for (const [name, text] of Object.entries(files))
      writeFileSync(join(directory, name), text)
    return await run('go', args, {
      cwd: directory,
      env: { ...env, GOWORK: 'off', GOTOOLCHAIN: 'local' },
    })
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}
