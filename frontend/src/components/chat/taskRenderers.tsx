/* eslint-disable solid/components-return-once -- render methods are not Solid components */
import type { MessageContentRenderer } from './messageRenderers'
import Check from 'lucide-solid/icons/check'
import { isObject } from './messageUtils'
import { ToolUseLayout } from './toolRenderers'
import {
  toolInputSummary,
} from './toolStyles.css'

/** Renders task_notification system messages as a tool-use-style block with Check icon. */
export const taskNotificationRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'task_notification')
      return null

    const status = typeof parsed.status === 'string' ? parsed.status : 'completed'
    const statusLabel = status.charAt(0).toUpperCase() + status.slice(1)
    const summaryText = typeof parsed.summary === 'string' ? parsed.summary : 'Task notification'
    const title = `${statusLabel}: ${summaryText}`
    const outputFile = typeof parsed.output_file === 'string' ? parsed.output_file : null
    const summary = outputFile ? <div class={toolInputSummary}>{outputFile}</div> : undefined

    return (
      <ToolUseLayout
        icon={Check}
        toolName="Task Notification"
        title={title}
        summary={summary}
        context={context}
      />
    )
  },
}
