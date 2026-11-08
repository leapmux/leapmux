import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath, pathToFileURL } from 'node:url'

export function assertHostedRunner(env) {
  if (env.GITHUB_ACTIONS !== 'true' || env.RUNNER_ENVIRONMENT !== 'github-hosted'
    || !/^[1-9]\d*$/.test(env.GITHUB_RUN_ID ?? '') || !/^[1-9]\d*$/.test(env.GITHUB_RUN_ATTEMPT ?? '')
    || !/^[\w.-]+\/[\w.-]+$/.test(env.GITHUB_REPOSITORY ?? '') || !env.RUNNER_TEMP) {
    throw new Error('Native service CI fixtures require a GitHub-hosted disposable runner')
  }
  if (!path.isAbsolute(env.RUNNER_TEMP))
    throw new Error('The disposable runner temporary directory must be absolute')
}

export function nativeCLISelection(hasDesktop) {
  return hasDesktop ? '^TestNativeServiceCLIConfiguration$' : '^TestNativeServiceCLIConfiguration/[^/]+/boot$'
}

// Always attempt both cleanup operations and retain the original test failure.
export function withFixtureCleanup(prepare, verify, cleanup, remove) {
  const failures = []
  try {
    prepare()
    verify()
  }
  catch (error) {
    failures.push(error)
  }
  for (const operation of [cleanup, remove]) {
    try {
      operation()
    }
    catch (error) {
      failures.push(error)
    }
  }
  if (failures.length === 1)
    throw failures[0]
  if (failures.length > 1)
    throw new AggregateError(failures, 'Native CLI verification or fixture cleanup failed')
}

function checked(program, args, options) {
  const result = spawnSync(program, args, { stdio: 'inherit', ...options })
  if (result.error)
    throw result.error
  if (result.status !== 0) {
    throw new Error(`${program} exited with ${result.status}${result.signal
      ? ` (${result.signal})`
      : ''}`)
  }
}

export function main() {
  assertHostedRunner(process.env)
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
  const runHelper = args => checked('go', ['run', './internal/servicelaunch/servicefixture/cmd/native-ci-fixture', ...args], {
    cwd: path.join(root, 'backend'),
  })
  let hasDesktop = process.platform === 'win32'
  if (process.platform === 'darwin')
    hasDesktop = spawnSync('/bin/launchctl', ['print', `gui/${os.userInfo().uid}`], { stdio: 'ignore' }).status === 0
  if (process.platform === 'linux')
    hasDesktop = spawnSync('/usr/bin/systemctl', ['--user', 'is-active', 'graphical-session.target'], { stdio: 'ignore' }).status === 0
  const directory = mkdtempSync(path.join(process.env.RUNNER_TEMP, 'leapmux-native-cli-'))
  const fixture = path.join(directory, 'account.yaml')
  withFixtureCleanup(
    () => runHelper(['prepare', fixture]),
    () => {
      console.log(`Native CLI service E2E selection: ${process.platform}, ${hasDesktop ? 'login and boot' : 'boot'}`)
      checked(process.platform === 'win32'
        ? 'task.exe'
        : 'task', [
        'test-cli-services',
        `SERVICE_ACCOUNT_FILE=${fixture}`,
        '--',
        '-run',
        nativeCLISelection(hasDesktop),
        '-count=1',
        '-timeout=20m',
      ], { cwd: root })
    },
    () => runHelper(['cleanup']),
    () => rmSync(directory, { recursive: true, force: true }),
  )
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  main()
