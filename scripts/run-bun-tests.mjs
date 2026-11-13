import { spawnSync } from 'node:child_process'
import { constants } from 'node:os'
import { resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { testReportPath } from './test-results-path.mjs'

/** Keep Bun's console output and add JUnit when a CI suite owns a results directory. */
export function runBunTests(label, args, { env = process.env, run = spawnSync } = {}) {
  const report = testReportPath(label, env)
  const result = run('bun', [
    'test',
    ...(report
      ? ['--reporter=junit', `--reporter-outfile=${report}`]
      : []),
    ...args,
  ], { env, stdio: 'inherit' })
  if (result.error)
    throw result.error
  return result.status ?? (result.signal
    ? 128 + (constants.signals[result.signal] ?? 1)
    : 1)
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    process.exitCode = runBunTests(process.argv[2], process.argv.slice(3))
  }
  catch (error) {
    console.error(error)
    process.exitCode = 1
  }
}
