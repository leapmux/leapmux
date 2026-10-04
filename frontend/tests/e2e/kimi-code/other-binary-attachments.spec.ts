import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { nativeTextStep } from '../helpers/nativeScenario'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest('refuses the attachment kind and excludes its actual bytes from the next native request', async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
  const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'kimi-code-refused.bin' })
  await expectRefusedAttachmentsAbsent(context.page, context.modelScript, [rejected], nativeTextStep(context, 'The clean prompt answered.'))
})
