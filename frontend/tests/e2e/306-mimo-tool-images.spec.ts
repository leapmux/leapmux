import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { readToolCall } from './helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from './helpers/toolImages'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

const MIMO = AgentProvider.MIMO_CODE

mimoTest.describe('MiMo Code images in tool results', () => {
  // The mock scripts the Read call. The picture in the tool row is produced by
  // the CLI reading the PNG and by LeapMux rendering that result.
  mimoTest('a Read of a PNG draws the picture in the tool row', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    const workingDir = authenticatedMiMoWorkspace.workingDir
    expect(workingDir, 'the agent workspace must expose a working directory').toBeTruthy()
    const name = writeToolImage(workingDir!, 'mimo-64')

    await modelScript.queue(
      { toolCalls: [readToolCall(MIMO, 'read-png', name)] },
      { text: `I opened ${name}.` },
    )
    await sendMessage(page, modelScript.prompt(`Read the file ${name} and describe it.`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectToolRowImage(page, 'tool-image-mimo-64')
  })
})
