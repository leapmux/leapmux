import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'

ampTest('proves the native slash text path has no completed compaction notice', async ({ authenticatedAmpWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  await expectNoCompactionNotice(context, { relatedProof: async () => {
    const old = 'AMP_UNCOMPACTED_NATIVE_CONTEXT'
    await sendNativeAnswer(context, 'Keep the native context marker.', old)
    await modelScript.queue({ text: 'The slash command remained ordinary text.' })
    await sendMessage(page, '/compact')
    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    const request = status.requests.find(record => record.stepIndex === 1)
    if (!request)
      throw new Error('The compact command reached no native Amp request.')
    expect(nativeModelContextText(request)).toContain('/compact')
    expect(nativeModelContextText(request)).toContain(old)
  } })
})
