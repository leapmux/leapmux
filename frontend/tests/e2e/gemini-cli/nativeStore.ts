import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { getGlobalState } from '../helpers/server'

/** Verify the native registry and project marker before reading a session path. */
export function geminiNativeProject(context: ManagedNativeScenarioContext, agent: AgentInfo): string {
  const home = context.leapmuxServer.agentEnv?.GEMINI_CLI_HOME
  if (!home || !agent.workingDir || !agent.agentSessionId)
    throw new Error('The native Gemini session has no private home or identity.')
  assertPrivateNativePath(home, getGlobalState().tmpDir)
  const root = join(home, '.gemini')
  const registry: unknown = JSON.parse(readFileSync(join(root, 'projects.json'), 'utf8'))
  if (!isObject(registry) || !isObject(registry.projects))
    throw new Error('The native Gemini registry is invalid.')
  const slug = registry.projects[agent.workingDir]
  if (typeof slug !== 'string' || !/^[a-z0-9-]+$/.test(slug))
    throw new Error('The native Gemini registry has no exact project owner.')
  const project = join(root, 'tmp', slug)
  assertPrivateNativePath(project, getGlobalState().tmpDir)
  if (readFileSync(join(project, '.project_root'), 'utf8').trim() !== agent.workingDir)
    throw new Error('The native Gemini project marker belongs to another directory.')
  return project
}

/** The clock that a minute wait reads and sleeps on. A unit test supplies its own. */
export interface MinuteClock {
  now: () => number
  sleep: (milliseconds: number) => Promise<void>
}

const realClock: MinuteClock = {
  now: () => Date.now(),
  sleep: milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds)),
}

const MINUTE_SPELLING = /^(\d{4}-\d{2}-\d{2})T(\d{2})-(\d{2})$/

/**
 * The minute in the name of each archive of one session.
 *
 * Gemini CLI names a root session archive `session-<UTC minute>-<first 8 characters of the session ID>`,
 * with `.jsonl` (or `.json` for an archive from a release before JSONL). A name of another shape is
 * not an archive of the session.
 */
export function geminiSessionArchiveMinutes(names: readonly string[], sessionId: string): string[] {
  const short = sessionId.slice(0, 8)
  if (!/^[\w-]{8}$/.test(short))
    throw new Error('The native Gemini session ID is too short or holds characters that an archive name cannot.')
  const minutes: string[] = []
  for (const name of names) {
    const match = /^session-(\d{4}-\d{2}-\d{2}T\d{2}-\d{2})-([\w-]{8})\.jsonl?$/.exec(name)
    if (match?.[1] && match[2] === short)
      minutes.push(match[1])
  }
  return minutes
}

/** Wait until the UTC minute after `minute` (in the archive spelling) begins. */
export async function waitForMinuteAfter(minute: string, clock: MinuteClock = realClock): Promise<void> {
  const parts = MINUTE_SPELLING.exec(minute)
  const start = parts ? Date.parse(`${parts[1]}T${parts[2]}:${parts[3]}:00.000Z`) : Number.NaN
  if (Number.isNaN(start))
    throw new Error(`The native Gemini archive minute ${JSON.stringify(minute)} is not a UTC minute.`)
  const end = start + 60_000
  for (let remaining = end - clock.now(); remaining > 0; remaining = end - clock.now())
    await clock.sleep(remaining)
}

/**
 * Wait until a later UTC minute than the newest archive of the stored session.
 *
 * Gemini CLI 0.62.0 cannot load a session in a minute that names one of its archives. Its
 * session/load starts a new chat for the same session ID before it looks the session up
 * (AcpSessionManager.loadSession, then ChatRecordingService.initialize). That chat takes the
 * name `session-<current minute>-<id8>.jsonl`, so in the archive's own minute it appends to
 * the stored archive, and its `$set` of the startup messages replaces the stored history. The
 * lookup then fails with -32603 "No previous sessions found for this project", and the stored
 * history stays lost. Upstream fixed this in google-gemini/gemini-cli#29463 (first release
 * v0.63.0-preview.0). A reader who reopens a session in a later minute is not affected.
 */
export async function waitForGeminiArchiveMinuteToPass(context: ManagedNativeScenarioContext, agent: AgentInfo, clock: MinuteClock = realClock): Promise<void> {
  const minutes = geminiSessionArchiveMinutes(readdirSync(join(geminiNativeProject(context, agent), 'chats')), agent.agentSessionId)
  const newest = minutes.reduce<string | undefined>((latest, minute) => latest === undefined || minute > latest ? minute : latest, undefined)
  if (newest === undefined)
    throw new Error('The native Gemini session has no archive to reopen.')
  await waitForMinuteAfter(newest, clock)
}
