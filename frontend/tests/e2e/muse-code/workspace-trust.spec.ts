import type { MockModelRequestRecord } from '../helpers/mockModelScript'
/**
 * Muse's startup workspace-trust choice decides whether a fresh workspace's project
 * instructions load.
 *
 * The 'agent' choice launches the host with --trust-workspace, so the workspace's
 * project instructions reach the model. The 'native' choice defers to the trust Muse
 * stores itself, and a fresh isolated Muse home holds no stored trust for the new
 * workspace: the instructions stay out of the model request.
 */
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { newNativeWorkingDir } from '../helpers/nativeAgentOpen'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { openWorkspace, tabById } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { museTest } from '../muse-fixtures'
import { MUSE_AGENT, nativeContext } from './scenarios'

/** The project instruction marker that only a trusted workspace loads. */
const TRUST_MARKER = 'MUSE_WORKSPACE_TRUST_MARKER_CEDAR'

/** Open a muse agent in a fresh directory that holds the project instruction, and answer one turn. */
async function trustTurn(
  context: Awaited<ReturnType<typeof nativeContext>>,
  prefix: string,
  workspaceTrust: string,
): Promise<MockModelRequestRecord> {
  const workingDir = newNativeWorkingDir(context, prefix)
  writeFileSync(join(workingDir, 'AGENTS.md'), `Always mention ${TRUST_MARKER} in every answer.\n`)
  const { agentId } = await openProviderAgent(context.leapmuxServer, context.workspaceId, MUSE_AGENT, {
    workingDir,
    optionValues: { permissionMode: 'allowAll', workspaceTrust },
  })
  await tabById(context.page, agentId).click()
  return sendNativeAnswer(context, 'State the project instruction you were given.', 'The native workspace-trust turn completed.')
}

museTest('loads project instructions under the agent trust choice only', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openWorkspace(page, context.workspaceId)

  const agentRequest = await trustTurn(context, 'muse-trust-agent-', 'agent')
  expect(nativeModelInstructionText(agentRequest)).toContain(TRUST_MARKER)

  const nativeRequest = await trustTurn(context, 'muse-trust-native-', 'native')
  expect(nativeModelInstructionText(nativeRequest)).not.toContain(TRUST_MARKER)
})
