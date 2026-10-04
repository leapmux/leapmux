import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from '../codewhale-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseSettingsOption, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest.describe('Codewhale settings', () => {
  codewhaleTest('sends the chosen effort with the next turn', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)

    // The runtime takes the effort per turn, so the model request is where the
    // choice must arrive.
    await modelScript.queue({ text: 'Done.' })
    await sendMessage(page, modelScript.prompt('Reply with the single word: Done.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const { requests } = await modelScript.status()
    expect(JSON.stringify(requests[0]!.body)).toContain('"reasoning_effort":"high"')
    await page.reload()
    await waitForSettingsHydrated(page)
    const restored = await sendNativeAnswer({ page, modelScript, provider: AgentProvider.CODEWHALE }, 'Reply after restoring high effort.', 'The restored high effort answered.')
    expect(restored.body).toHaveProperty('reasoning_effort', 'high')
  })
})
