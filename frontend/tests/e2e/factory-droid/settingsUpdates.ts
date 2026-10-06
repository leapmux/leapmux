import type { DroidNativeSettingsUpdate } from '../helpers/droidSettingsFrame'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { decompressContentToString } from '../../../src/lib/decompress'
import { parseDroidNativeSettingsUpdates } from '../helpers/droidSettingsFrame'
import { readAllAgentMessages } from '../helpers/nativeMessages'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'

/** The prefix of the request ID of a settings change that LeapMux sends. Droid copies the ID into its settings event. */
const LEAPMUX_REQUEST_PREFIX = 'leapmux-'

/** The settings fields of a Droid settings event that a check can require. */
export type DroidSettings = Partial<Omit<DroidNativeSettingsUpdate, 'requestId'>>

/**
 * Which settings event must state the expected settings:
 * - `leapmux` (the default): any event that answers a settings request of LeapMux.
 * - `latest`: the last event that states each expected field, whatever sent the request. Use it for a change that
 *   Droid makes by itself, such as the end of Spec mode after a plan approval.
 */
export type DroidSettingsEvent = 'leapmux' | 'latest'

/** Return the fields of `expected`, and refuse an expectation that states none. */
function expectedSettingKeys(expected: DroidSettings): (keyof DroidSettings)[] {
  const keys = (Object.keys(expected) as (keyof DroidSettings)[]).filter(key => expected[key] !== undefined)
  if (keys.length === 0)
    throw new Error('The Droid settings check needs at least one expected setting.')
  return keys
}

/** Return whether `updates` hold the expected settings in the event that `event` selects. Pure, for a unit test. */
export function droidSettingsMatch(updates: readonly DroidNativeSettingsUpdate[], expected: DroidSettings, event: DroidSettingsEvent = 'leapmux'): boolean {
  const keys = expectedSettingKeys(expected)
  const holds = (update: DroidNativeSettingsUpdate) => keys.every(key => update[key] === expected[key])
  if (event === 'leapmux')
    return updates.some(update => update.requestId?.startsWith(LEAPMUX_REQUEST_PREFIX) === true && holds(update))
  const latest = updates.filter(update => keys.every(key => update[key] !== undefined)).at(-1)
  return latest !== undefined && holds(latest)
}

/**
 * Read every settings event that the installed Droid reported to the Worker for one agent, oldest first.
 * The read takes every page of the stored messages, so a long transcript loses no event.
 */
export async function readDroidNativeSettings(context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>, agentId: string): Promise<DroidNativeSettingsUpdate[]> {
  const messages = await readAllAgentMessages(context, agentId)
  return messages.flatMap((message) => {
    const raw = decompressContentToString(message.content, message.contentCompression)
    return raw ? parseDroidNativeSettingsUpdates(raw) : []
  })
}

/**
 * Wait until Droid reports the expected settings for the agent on screen, in the event that `event` selects.
 * A failure prints the settings events that Droid reported.
 */
export async function expectDroidNativeSettings(
  context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>,
  expected: DroidSettings,
  event: DroidSettingsEvent = 'leapmux',
): Promise<void> {
  expectedSettingKeys(expected)
  const agentId = await selectedAgentTabId(context.page)
  // The failure states the last error: the failed Worker read, or the settings events that the last read returned.
  await retryUntilPass(async () => {
    const updates = await readDroidNativeSettings(context, agentId)
    if (!droidSettingsMatch(updates, expected, event))
      throw new Error(`Droid reported no ${event} settings event that states ${JSON.stringify(expected)}: it reported ${JSON.stringify(updates)}.`)
  })
}
