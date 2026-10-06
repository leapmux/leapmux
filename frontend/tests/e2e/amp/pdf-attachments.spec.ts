import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { nativeTextStep } from '../helpers/nativeScenario'

ampTest('refuses the attachment kind and excludes its actual bytes from the next native request', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false, fileName: 'amp-refused.pdf', readyGroup: 'agent_mode' })
  await expectRefusedAttachmentsAbsent(context.page, context.modelScript, [rejected], nativeTextStep(context, 'The clean prompt answered.'))
})
