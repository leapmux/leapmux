import type { TriggerRequest } from '../../ir/tools/trigger'
import CalendarClock from 'lucide-solid/icons/calendar-clock'
import { proseRenderer, typedRequestLine } from './proseResult'

/** What one trigger action says it does, in the words the tool itself uses. */
function triggerTitle(request: TriggerRequest): string {
  switch (request.action) {
    case 'list': return 'List triggers'
    case 'get': return request.triggerId ? `Get trigger ${request.triggerId}` : 'Get trigger'
    case 'create': return request.name ? `Create trigger: ${request.name}` : 'Create trigger'
    case 'update': {
      const head = request.triggerId ? `Update trigger ${request.triggerId}` : 'Update trigger'
      return request.name ? `${head}: ${request.name}` : head
    }
    case 'run': return request.triggerId ? `Run trigger ${request.triggerId}` : 'Run trigger'
    case 'delete': return request.triggerId ? `Delete trigger ${request.triggerId}` : 'Delete trigger'
    default: return ''
  }
}

export const triggerRenderer = proseRenderer<'trigger'>({
  icon: CalendarClock,
  label: 'Trigger',
  title(call) {
    // A finished call words its header with the endpoint's answer; a running
    // one composes the action it runs.
    //
    // Both branches end in the kind's own label, as the other twenty-nine do. An
    // action word this switch lists in no case -- `other` -- composes '', and a call
    // that also carried no title of its own then drew a bare icon and no words.
    return call.result !== undefined
      ? (call.title || triggerTitle(call.request) || 'Trigger')
      : (triggerTitle(call.request) || call.title || 'Trigger')
  },
  // The SCHEDULE, which no title states. It is the one fact a cron entry exists for,
  // and every provider fills it -- so a row that never drew it left the reader with a
  // scheduled job and no answer to when it runs.
  //
  // It draws while the call runs, which is the one state `typedRequestLine` is for and
  // the rule `thinkRenderer` follows for its own line. A row that holds the answer
  // draws that answer instead, so one span draws the line exactly once: a paired
  // request carries no result, and the result row beside it carries one.
  request(call) {
    if (call.result !== undefined)
      return null
    return typedRequestLine([call.request.schedule])
  },
})
