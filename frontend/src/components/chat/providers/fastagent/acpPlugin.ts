import { FASTAGENT_AUDIO_MIME, FASTAGENT_MODE } from '~/generated/contracts/fastagent-protocol'
import { createACPProvider } from '../acp/registerACPProvider'
import { fastAgentToolCallAdapter } from './extractors/toolCall'
import { fastAgentPermissionPresets } from './permissionPresets'

/** Preserve genuine ACP behavior before composing native history readers. */
export const fastAgentACPPlugin = createACPProvider({
  permissionPresets: fastAgentPermissionPresets,
  toolCallAdapter: fastAgentToolCallAdapter,
  defaultPermissionMode: FASTAGENT_MODE.Agent,
  attachments: { text: true, image: true, pdf: true, audio: [...FASTAGENT_AUDIO_MIME], binary: false },
})
