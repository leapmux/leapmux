import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { nativeTextStep } from '../helpers/nativeScenario'

codewhaleTest('refuses the attachment kind and excludes its actual bytes from the next native request', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'codewhale-refused.bin' })
  await expectRefusedAttachmentsAbsent(context.page, context.modelScript, [rejected], nativeTextStep(context, 'The clean prompt answered.'))
})
