import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clineTest } from '../cline-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { nativeTextStep } from '../helpers/nativeScenario'

clineTest('refuses the attachment kind and excludes its actual bytes from the next native request', async ({ page, modelScript, leapmuxServer, authenticatedClineWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
  const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'cline-refused.bin' })
  await expectRefusedAttachmentsAbsent(context.page, context.modelScript, [rejected], nativeTextStep(context, 'The clean prompt answered.'))
})
