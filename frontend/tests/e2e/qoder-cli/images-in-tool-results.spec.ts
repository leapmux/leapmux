import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expect } from '../fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI images in tool results', () => {
  qoderTest('renders an image returned by the native Read tool', async ({ qoderWorkspace, page, modelScript }) => {
    const fileName = writeToolImage(qoderWorkspace.workingDir, 'qoder-348')
    const filePath = join(qoderWorkspace.workingDir, fileName)
    await modelScript.queue(
      { toolCalls: [readToolCall(AgentProvider.QODER, 'read-qoder-image', filePath)] },
      { text: 'The image was read.' },
    )
    await sendMessage(page, modelScript.prompt('Read the local image file.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body ?? {}).includes('iVBORw0KGgo')).toBe(true)
    await expectToolRowImage(page, fileName)
  })
})
