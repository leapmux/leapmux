import { join } from 'node:path'
import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowWithoutImage, writeToolImage } from '../helpers/toolImages'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openQwenAgent, QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.QWEN_CODE

qwenTest.describe('Qwen Code tool execution', () => {
  qwenTest('shows the native image overview without an inline image', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'yolo' })
    const name = writeToolImage(workingDir, 'qwen-read')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await modelScript.queue(
      { toolCalls: [readToolCall(PROVIDER, 'read-image', join(workingDir, name))] },
      { text: 'The image read finished.' },
    )
    await sendMessage(page, modelScript.prompt('Read the PNG file.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const second = status.requests.find(request => request.stepIndex === 1)
    expect(second?.protocol).toBe('openai-chat-completions')
    expect(JSON.stringify(second?.body).includes('data:image/jpeg;base64,/9j/')).toBe(true)
    await expectToolRowWithoutImage(page, name)
    await expect(page.locator('[data-chat-scroll-container="true"]:visible').getByText(/Image overview: 64x64/).first()).toBeVisible()
  })
})
