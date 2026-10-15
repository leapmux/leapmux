import { readFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { getGlobalState } from '../helpers/server'

/** Check the task link that native session/load resolves through the history index. */
export function hasDiracResumeCheckpoint(runtime: unknown, history: unknown, sessionId: string): boolean {
  if (!sessionId.trim() || !isObject(runtime) || typeof runtime.taskId !== 'string' || !runtime.taskId.trim() || !Array.isArray(history))
    return false
  const matches = history.filter(row => isObject(row) && row.id === runtime.taskId)
  return matches.length === 1 && matches[0].ulid === sessionId
}

/**
 * Require a persisted native checkpoint before testing stored-context resume.
 * Dirac 0.5.20 exits on stdin EOF without flushing its debounced history index.
 * The immediate-close defect is tracked at https://github.com/leapmux/leapmux/issues/528.
 */
export async function waitForDiracCheckpoint(profileDir: string, sessionId: string): Promise<void> {
  if (!isAbsolute(profileDir) || !/^[a-z0-9][\w-]*$/i.test(sessionId))
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
