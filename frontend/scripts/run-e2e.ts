import type { SpawnOptions } from 'node:child_process'
import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { delimiter, dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { withCleanup } from '../tests/e2e/helpers/cleanup'
import { stopTrackedProcesses } from '../tests/e2e/helpers/processRegistry'
import { copyRunBinary, LEAPMUX_BINARY_NAME } from '../tests/e2e/helpers/runBinary'
import { resolveTaskBin } from './resolve-task-bin'

const require = createRequire(import.meta.url)
const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')

export function runCommand(cmd: string, args: string[], options: SpawnOptions = {}): Promise<number> {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, {
      ...options,
      stdio: 'inherit',
      env: options.env ?? process.env,
    })
    child.once('error', reject)
    child.once('exit', code => resolve(code ?? 1))
  })
}

/**
 * Build the backend, then run Playwright with `args` in a private run directory.
 *
 * `projectRoot` is the repository root. A test gives its own directory, so the
 * test neither reads nor writes the real build output.
 */
export async function runE2E(args: string[], projectRoot: string = root): Promise<number> {
  // The build cache includes this flag, which enables the browser timing instrumentation.
  const env: NodeJS.ProcessEnv = { ...process.env, LEAPMUX_DEV: '1', E2E_STATE_PATH: undefined }
  const buildCode = await runCommand(resolveTaskBin(), ['build-backend'], { cwd: projectRoot, env })
  if (buildCode !== 0)
    return buildCode

  const scratch = join(projectRoot, '.tmp')
  mkdirSync(scratch, { recursive: true })
  const runDir = mkdtempSync(join(scratch, 'e2e-'))
  return withCleanup(async () => {
    // Copy the binary at once, before another task pipeline can rebuild it.
    // Global setup gives this copy to every process of the run. See runBinaryPath.
    copyRunBinary(join(projectRoot, LEAPMUX_BINARY_NAME), runDir)
    // Global setup verifies the nonce before it starts fixtures.
    const noncePath = join(runDir, 'nonce')
    const nonce = crypto.randomUUID()
    writeFileSync(noncePath, nonce)
    const testEnv: NodeJS.ProcessEnv = {
      ...env,
      LEAPMUX_E2E_NONCE_PATH: noncePath,
      LEAPMUX_E2E_NONCE: nonce,
      // A directory under .tmp must not inherit the project's Git repository.
      GIT_CEILING_DIRECTORIES: [runDir, env.GIT_CEILING_DIRECTORIES].filter(Boolean).join(delimiter),
    }
    for (const key of ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_COMMON_DIR', 'GIT_INDEX_FILE', 'GIT_OBJECT_DIRECTORY', 'GIT_ALTERNATE_OBJECT_DIRECTORIES'])
      delete testEnv[key]
    return await runCommand('node', [require.resolve('@playwright/test/cli'), 'test', ...args], {
      cwd: join(projectRoot, 'frontend'),
      env: testEnv,
    })
  }, async () => {
    // Playwright cannot run global teardown after a forced process exit.
    await stopTrackedProcesses(runDir)
    rmSync(runDir, { recursive: true, force: true })
  })
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    process.exitCode = await runE2E(process.argv.slice(2))
  }
  catch (error) {
    console.error(error)
    process.exitCode = 1
  }
}
