/**
 * ZCode fixtures use the shared agent workspace lifetime.
 * The skip check requires a launcher or bundled script and a usable provider configuration.
 * These checks match the worker's launch requirements.
 */
import { existsSync, readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import process from 'node:process'
import { findBinary, unusableBinaryReason } from './helpers/binaryOnPath'
import { hubSpawnEnv } from './helpers/server'
import { createProviderTest } from './provider-fixture-factory'
import { computeZCodeE2ESkipReason } from './zcode-install'
import { nativeContext, ZCODE_AGENT } from './zcode/scenarios'

// Find the launcher without running it. This check runs nothing in the developer's HOME.
// See `helpers/binaryOnPath.ts`.
const ZCODE_LAUNCHER = findBinary('zcode')

export const ZCODE_E2E_SKIP_REASON: string | null = computeZCodeE2ESkipReason({
  scriptOverride: process.env.LEAPMUX_ZCODE_SCRIPT,
  scriptExists: existsSync,
  launcherOnPath: ZCODE_LAUNCHER !== null,
  launcherUnusableReason: ZCODE_LAUNCHER === null ? null : unusableBinaryReason('zcode', ZCODE_LAUNCHER),
  platform: process.platform,
  home: homedir(),
  env: hubSpawnEnv(),
  readConfig: path => (existsSync(path) ? readFileSync(path, 'utf-8') : null),
})

export const zcodeTest = createProviderTest({
  agent: ZCODE_AGENT,
  nativeContext,
  skipReason: ZCODE_E2E_SKIP_REASON,
  workspaceName: 'authenticatedZCodeWorkspace',
})
