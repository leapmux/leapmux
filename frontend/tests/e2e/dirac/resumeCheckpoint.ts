import { readFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { getGlobalState } from '../helpers/server'

const nativePathComponent = /^[a-z0-9][\w-]*$/i

/** Check the task link that native session/load resolves through the history index. */
export function hasDiracResumeCheckpoint(runtime: unknown, history: unknown, sessionId: string): boolean {
  if (!nativePathComponent.test(sessionId) || !isObject(runtime) || typeof runtime.taskId !== 'string'
    || !nativePathComponent.test(runtime.taskId) || !isObject(runtime.settings)
    || (runtime.settings.mode !== 'act' && runtime.settings.mode !== 'plan') || !Array.isArray(history)) {
    return false
  }
  const matches = history.filter(row => isObject(row) && row.id === runtime.taskId)
  return matches.length === 1 && matches[0].ulid === sessionId
}

/**
 * Wait for the native runtime link and its matching history row.
 * The immediate-close regression calls this after the Worker closes the tab.
 */
export async function waitForDiracCheckpoint(profileDir: string, sessionId: string): Promise<void> {
  if (!isAbsolute(profileDir) || !nativePathComponent.test(sessionId))
    throw new Error('The Dirac checkpoint requires an absolute profile directory and a safe session ID.')
  const runDir = getGlobalState().tmpDir
  assertPrivateNativePath(profileDir, runDir)
  const runtimePath = join(profileDir, 'data', 'acp-session-runtime-configs', `${sessionId}.json`)
  const historyPath = join(profileDir, 'data', 'state', 'taskHistory.json')
  await retryUntilPass(() => {
    assertPrivateNativePath(runtimePath, runDir)
    assertPrivateNativePath(historyPath, runDir)
    const runtime: unknown = JSON.parse(readFileSync(runtimePath, 'utf8'))
    const history: unknown = JSON.parse(readFileSync(historyPath, 'utf8'))
    if (!hasDiracResumeCheckpoint(runtime, history, sessionId))
      throw new Error('The Dirac history index does not contain the current native task and session.')
  })
}
