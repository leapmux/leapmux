// Guard script: ensures this project is only installed/run with Bun.
// Bun sets npm_config_user_agent to "bun/<version> ..." in lifecycle scripts.

import { existsSync, rmSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { env, exit } from 'node:process'
import { fileURLToPath } from 'node:url'

const ua = env.npm_config_user_agent || ''
if (!ua.startsWith('bun/')) {
  // Clean up package-lock.json that npm creates before preinstall runs.
  const projectRoot = dirname(dirname(fileURLToPath(import.meta.url)))
  const lockFile = join(projectRoot, 'package-lock.json')
  if (existsSync(lockFile)) {
    rmSync(lockFile)
  }

  console.error(
    'This project requires Bun. Use `bun install` / `bun run` instead of npm/pnpm/yarn.',
  )
  exit(1)
}
