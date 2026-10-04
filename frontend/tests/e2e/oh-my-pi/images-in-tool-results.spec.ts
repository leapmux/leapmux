import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The test calls a real native image tool. The correlated completed result must contain a decoded image.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest.describe('Oh My Pi tool execution', () => {
  ohMyPiTest('draws a PNG returned by its Read tool', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    const workingDir = authenticatedOhMyPiWorkspace.workingDir
    if (!workingDir)
      throw new Error('the Oh My Pi fixture needs a working directory')
    const name = writeToolImage(workingDir, 'omp-read')
    await modelScript.queue(
      { toolCalls: [readToolCall(AgentProvider.OH_MY_PI, 'read-image', join(workingDir, name))] },
      { text: 'The image read finished.' },
    )
    await sendMessage(page, modelScript.prompt('Read the PNG file.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectToolRowImage(page, name)
  })
})
