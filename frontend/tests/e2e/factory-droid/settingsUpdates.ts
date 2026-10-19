import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { DroidNativeSettingsSnapshot } from './settingsSnapshot'
import { readFileSync } from 'node:fs'
import { isAbsolute, join, win32 } from 'node:path'
import { AgentProvider, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sameAgentOwnership } from '../helpers/nativeMessages'
import { nativeAgentById, selectedAgentTabId } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { isFileNameComponent } from '../helpers/runDirectory'
import { parseDroidNativeSettingsSnapshot } from './settingsSnapshot'

/** The effective native settings that a check requires. */
export type DroidSettings = Partial<DroidNativeSettingsSnapshot>

/** Return the fields of `expected`, and refuse an expectation that states none. */
function expectedSettingKeys(expected: DroidSettings): (keyof DroidSettings)[] {
  const keys = (Object.keys(expected) as (keyof DroidSettings)[]).filter(key => expected[key] !== undefined)
  if (keys.length === 0)
    throw new Error('The Droid settings check needs at least one expected setting.')
  return keys
}

/** Check each requested field against the current native snapshot. */
export function droidSettingsMatch(snapshot: DroidNativeSettingsSnapshot, expected: DroidSettings): boolean {
  const keys = expectedSettingKeys(expected)
  return keys.every(key => snapshot[key] === expected[key])
}

/**
 * Read Droid's own settings file for the exact current session.
 * The Worker consumes settings_updated frames, so the transcript cannot prove the effective settings.
 * Ownership must remain unchanged across this file read.
 */
export async function readDroidNativeSettings(context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>, agentId: string): Promise<DroidNativeSettingsSnapshot> {
  const factoryHome = context.leapmuxServer.agentEnv?.FACTORY_HOME_OVERRIDE
  if (!factoryHome || !isAbsolute(factoryHome))
    throw new Error('The Droid settings check requires an isolated absolute Factory home.')
  const before = await nativeAgentById(context, agentId)
  if (!before || before.id !== agentId || before.workerId !== context.leapmuxServer.workerId
    || before.agentProvider !== AgentProvider.DROID || before.status !== AgentStatus.ACTIVE
    || !before.agentSessionId.trim() || !isFileNameComponent(before.agentSessionId)
    || (!isAbsolute(before.workingDir) && !win32.isAbsolute(before.workingDir))) {
    throw new Error('The Droid settings read requires an active agent with an exact native session and absolute working directory.')
  }
  // Droid uses this directory spelling on both platforms. The native session file supplies the values.
  const cwd = before.workingDir.replace(/[/\\]/g, '-').replaceAll(':', '')
  const path = join(factoryHome, '.factory', 'sessions', cwd, `${before.agentSessionId}.settings.json`)
  const snapshot = parseDroidNativeSettingsSnapshot(readFileSync(path, 'utf8'))
  const after = await nativeAgentById(context, agentId)
  if (!after || after.status !== AgentStatus.ACTIVE || !sameAgentOwnership(before, after))
    throw new Error('The Droid session or agent ownership changed during the native settings read.')
  return snapshot
}

/**
 * Wait until the selected native session stores the expected settings.
 * A failure reports the last native snapshot or read error.
 */
export async function expectDroidNativeSettings(
  context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>,
  expected: DroidSettings,
): Promise<void> {
  expectedSettingKeys(expected)
  const agentId = await selectedAgentTabId(context.page)
  await retryUntilPass(async () => {
    const snapshot = await readDroidNativeSettings(context, agentId)
    if (!droidSettingsMatch(snapshot, expected))
      throw new Error(`Droid's native settings do not match ${JSON.stringify(expected)}: the file states ${JSON.stringify(snapshot)}.`)
  })
}
