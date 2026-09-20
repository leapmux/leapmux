import type { LucideIcon } from 'lucide-solid'
import type { ToolIconHint } from '../model/toolCall'
import Braces from 'lucide-solid/icons/braces'
import GitBranch from 'lucide-solid/icons/git-branch'
import ListChecks from 'lucide-solid/icons/list-checks'
import OctagonX from 'lucide-solid/icons/octagon-x'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import TicketsPlane from 'lucide-solid/icons/tickets-plane'
import Webhook from 'lucide-solid/icons/webhook'

/**
 * The glyph for each hint a call can state.
 *
 * Exhaustive over {@link ToolIconHint}, so a new hint fails to compile until
 * this file gives it a glyph. This is the ONE place a tool-call hint becomes an
 * icon component: the model states what the tool does, and the choice of glyph is
 * this layer's.
 */
const TOOL_HINT_ICON: Record<ToolIconHint, LucideIcon> = {
  'checklist': ListChecks,
  'stop': OctagonX,
  'plan-enter': TicketsPlane,
  'plan-exit': PlaneTakeoff,
  'webhook': Webhook,
  'branch': GitBranch,
  'json': Braces,
}

/** The icon one call's hint asks for; undefined when the call states no hint. */
export function toolHintIcon(hint: ToolIconHint | undefined): LucideIcon | undefined {
  return hint === undefined ? undefined : TOOL_HINT_ICON[hint]
}
