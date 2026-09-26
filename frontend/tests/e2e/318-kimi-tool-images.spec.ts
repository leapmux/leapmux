import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { readToolCall } from './helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from './helpers/toolImages'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, KIMI_E2E_SKIP_REASON, kimiTest } from './kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

const KIMI = AgentProvider.KIMI_CODE

kimiTest.describe('Kimi Code images in tool results', () => {
  kimiTest('a Read of a PNG draws the picture in the tool row', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    const workingDir = authenticatedKimiWorkspace.workingDir
    expect(workingDir, 'the agent workspace must expose a working directory').toBeTruthy()
    const name = writeToolImage(workingDir, 'kimi-42')

    await modelScript.queue(
      { toolCalls: [readToolCall(KIMI, 'read-png', name)] },
      { text: `I opened ${name}.` },
    )
    await sendMessage(page, modelScript.prompt(`Read the file ${name} and describe it.`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectToolRowImage(page, 'tool-image-kimi-42')
  })
})
