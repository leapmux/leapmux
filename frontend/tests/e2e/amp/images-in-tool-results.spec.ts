import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

/**
 * The test calls a real native image tool. The correlated completed result must contain a decoded image.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp tool execution', () => {
  ampTest('draws a PNG returned by its Read tool', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    const { workingDir } = authenticatedAmpWorkspace
    const name = writeToolImage(workingDir, 'amp-read')
    await modelScript.queue(
      { toolCalls: [readToolCall(AgentProvider.AMP, 'read-image', join(workingDir, name))] },
      { text: 'The image read finished.' },
    )
    await sendMessage(page, modelScript.prompt('Read the PNG file.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectToolRowImage(page, name)
  })
})
