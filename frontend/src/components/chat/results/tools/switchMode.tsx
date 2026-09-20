import Replace from 'lucide-solid/icons/replace'
import { toolCallStatusOutcome } from '../../model/toolCallStatus'
import { toolOutcomeLabel } from '../../results/toolOutcomeLabel'
import { proseRenderer } from './proseResult'

export const switchModeRenderer = proseRenderer<'switch_mode'>({
  icon: Replace,
  label: 'Switch Mode',
  title(call) {
    // The mode the session moved INTO, with the worktree it states when it has one.
    // `mode` alone read as the bare wire token, and both worktree tools stated the
    // same word, so the two rows were indistinguishable.
    const { mode, target } = call.request
    if (mode && target)
      return `${mode}: ${target}`
    return mode ?? call.title ?? 'Switch Mode'
  },
  // The words a REFUSAL states, which the request carries for the one tool whose
  // refusal is an answer rather than a failure. This layer knows no provider, so it
  // cannot decide which tool that is; a request that words nothing takes the shared
  // outcome word, which reads "Declined".
  outcomeTitle(call) {
    const outcome = toolCallStatusOutcome(call.status)
    if (outcome === 'declined' && call.request.declinedTitle)
      return call.request.declinedTitle
    return toolOutcomeLabel(outcome ?? 'failed')
  },
})
