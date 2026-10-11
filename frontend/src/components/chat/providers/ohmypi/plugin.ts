import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyOhMyPiMessage } from './classification'
import { ohMyPiCompactionBoundary, ohMyPiNotificationEntry } from './extractors/notification'
import { ohMyPiResultDivider } from './extractors/resultDivider'
import { ohMyPiExtractRow } from './extractors/row'
import { ohMyPiControls } from './pluginControls'
import { resolveOhMyPiMessage } from './resolveMessage'
import { ohMyPiValidateResumeHandle } from './resumeHandle'
import { ohMyPiContextUsageFromMessage } from './sessionMetadata'
import { ohMyPiRelatedMessages, ohMyPiSpanRole } from './spanRole'

const ohMyPiPlugin: ProviderPlugin = {
  transcript: {
    resolveMessage: resolveOhMyPiMessage,
    spanRole: ohMyPiSpanRole,
    relatedMessages: ohMyPiRelatedMessages,
    classify: classifyOhMyPiMessage,
    extractRow: ohMyPiExtractRow,
    notificationEntry: ohMyPiNotificationEntry,
    extractDivider: ohMyPiResultDivider,
  },
  controls: ohMyPiControls,
  session: {
    contextUsageFromMessage: ohMyPiContextUsageFromMessage,
    compactionBoundaryFromMessage: ohMyPiCompactionBoundary,
    // The worker reports the session FILE as the session id, so the UI shortens it to
    // its base name and labels the copy action "session file path".
    sessionIdIsFilePath: true,
    validateResumeHandle: ohMyPiValidateResumeHandle,
  },
  configuration: {
    // The same policy as the worker's ValidateAttachment: omp's prompt carries text
    // and images, and it has no field for a PDF or another binary file.
    attachments: {
      text: true,
      image: true,
      pdf: false,
      binary: false,
    },
    // omp's one mode axis is its tool approval mode.
    triggerModeGroupKey: 'permissionMode',
  },
}

registerProvider(AgentProvider.OH_MY_PI, ohMyPiPlugin)
