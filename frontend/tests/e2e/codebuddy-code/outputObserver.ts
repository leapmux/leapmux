import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { expect } from '@playwright/test'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { getGlobalState } from '../helpers/server'

/** Read only the selected native session's spool files, without predicting the runtime's project slug. */
export function codeBuddyCapturedOutputFiles(configDir: string, sessionId: string, output: string): string[] {
  if (!isAbsolute(configDir))
    throw new Error('The CodeBuddy output observer requires an absolute configuration directory.')
  if (!sessionId || !/^[\w-]+$/.test(sessionId) || !output.trim())
    throw new Error('The CodeBuddy output observer requires a native session ID and output marker.')
  const projects = join(configDir, 'projects')
  if (!existsSync(projects))
    return []
  return readdirSync(projects, { withFileTypes: true }).filter(entry => entry.isDirectory()).flatMap((entry) => {
    const directory = join(projects, entry.name, sessionId, 'tool-results')
    if (!existsSync(directory))
      return []
    assertPrivateNativePath(directory, configDir)
    return readdirSync(directory, { withFileTypes: true }).filter(file => file.isFile() && file.name.endsWith('.txt')).flatMap((file) => {
      const path = join(directory, file.name)
      return readFileSync(path, 'utf8').includes(output)
        ? [path]
        : []
    })
  })
}

/**
 * CodeBuddy writes its spool after it retains each chunk in its result buffers.
 * Its headless protocol exposes no live bytes, so the browser cannot release the hold.
 */
export async function codeBuddyOutputObserver(context: ManagedNativeScenarioContext): Promise<(output: string) => Promise<void>> {
  const configDir = context.leapmuxServer.agentEnv?.CODEBUDDY_CONFIG_DIR
  if (!configDir)
    throw new Error('The native CodeBuddy output observer requires its private configuration directory.')
  assertPrivateNativePath(configDir, getGlobalState().tmpDir)
  const agent = await currentNativeAgent(context)
  return async (output) => {
    await expect.poll(() => codeBuddyCapturedOutputFiles(configDir, agent.agentSessionId, output), {
      message: 'the selected CodeBuddy session retained the held command output',
    }).toHaveLength(1)
  }
}
