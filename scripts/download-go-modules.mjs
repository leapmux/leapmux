import { spawnSync } from 'node:child_process'
import { dirname, resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { GO_TOOL_MODULES } from './go-tools.mjs'

/** Populate the shared download cache without combining module graphs. */
export function downloadGoModules({ cwd = process.cwd(), env = process.env, run = spawnSync } = {}) {
  const directories = [
    cwd,
    ...GO_TOOL_MODULES.map(file => resolve(cwd, dirname(file))),
  ]
  for (const [index, directory] of directories.entries()) {
    const result = run('go', ['mod', 'download'], {
      cwd: directory,
      env: index === 0
        ? env
        : { ...env, GOWORK: 'off', GOFLAGS: '-mod=readonly' },
      stdio: 'inherit',
    })
    if (result.error)
      throw result.error
    if (result.status !== 0)
      return result.status ?? 1
  }
  return 0
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    process.exitCode = downloadGoModules()
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
