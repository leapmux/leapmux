import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowWithoutImage, writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code file tool execution', () => {
  const PROVIDER = AgentProvider.CODEBUDDY

  codebuddyTest('shows the native Read placeholder without an inline image', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    const { workingDir } = authenticatedCodebuddyWorkspace
    const name = writeToolImage(workingDir, 'codebuddy-read')
    await modelScript.queue(
      { toolCalls: [readToolCall(PROVIDER, 'read-image', join(workingDir, name))] },
      { text: 'The image read finished.' },
    )
    await sendMessage(page, modelScript.prompt('Read the PNG file.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const second = status.requests.find(request => request.stepIndex === 1)
    expect(second?.protocol).toBe('openai-chat-completions')
    expect(JSON.stringify(second?.body).includes('data:image/png;base64,iVBORw0KGgo')).toBe(true)
    await expectToolRowWithoutImage(page, name)
  })
})
