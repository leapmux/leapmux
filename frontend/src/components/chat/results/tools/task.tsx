import type { ToolKindRenderer } from './renderer'
import ClockFading from 'lucide-solid/icons/clock-fading'
import { taskResultCollapsible } from '../../ir/tools/task'
import { StatusResultBody } from '../statusResult'

export const taskRenderer: ToolKindRenderer<'task'> = {
  icon: ClockFading,
  label: 'Task',
  // `StatusResultBody` puts the task's own state word in its header. That word is
  // optional (`TaskResult.title`), and without it the header is a lone glyph.
  statesOwnOutcome: call => (call.result.title ?? '') !== '',
  title(call) {
    return call.title ?? 'Task'
  },
  result(call, view) {
    return <StatusResultBody source={call.result} {...(view.context !== undefined ? { context: view.context } : {})} />
  },
  resultMeta(call) {
    return {
      collapsible: taskResultCollapsible(call.result),
      hasDiff: false,
      copyableContent: () => call.result.output || null,
      previewText: () => [call.result.title, call.result.output].filter(Boolean).join(' - ') || null,
    }
  },
}
