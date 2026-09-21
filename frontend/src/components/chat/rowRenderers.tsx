import type { JSX } from 'solid-js'
import type { AgentPrompt } from './model/divider'
import type { ChatRow } from './model/row'
import type { ExpandableToolLayoutContext, RowRenderContext } from './renderContext'
import type { ChatRowExtraction } from './rowExtraction'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import MessageSquare from 'lucide-solid/icons/message-square'
import { untrack } from 'solid-js'
import { assertNever } from '~/lib/assertNever'
import { completionMarker } from './assembledMessage'
import {
  MarkdownText,
  PlanExecutionMessage,
  renderControlResponseRow,
  ThinkingMessage,
  UnrecognizedMessage,
  UserContentMessage,
  useSharedExpandedState,
} from './messageRenderers'
import { MESSAGE_UI_KEY } from './messageUiKeys'
import { flattenNotificationEntries } from './notificationEntries'
import { renderNotificationBlocks } from './notificationRenderers'
import { ResultDivider } from './resultDividerRenderers'
import { CollapsibleContent } from './results/CollapsibleContent'
import { ToolMessage } from './results/ToolMessage'
import { toolOutcomeLabel } from './results/toolOutcomeLabel'
import { ToolStatusHeader } from './results/ToolStatusHeader'
import { textNeedsCollapse } from './results/useCollapsedLines'
import { toolOutcomeNote } from './toolOutcome'
import { MarkdownPlanLayout } from './widgets/MarkdownPlanLayout'
import { ToolUseLayout } from './widgets/ToolUseLayout'

/**
 * Draw one row from the shared model.
 *
 * Layer 3 of the render pipeline, and the ONE place a row kind becomes markup. It
 * branches on the kind and on nothing else -- no provider, no tool name, no wire
 * shape -- because layer 1 already answered those.
 *
 * EXHAUSTIVE: a new row kind is a compile error here rather than a row that silently
 * draws nothing.
 */
export function renderRowContent(
  row: ChatRow,
  context: RowRenderContext | undefined,
): JSX.Element {
  switch (row.kind) {
    case 'tool':
      return (
        <ToolMessage
          row={row}
          {...(context !== undefined ? { context } : {})}
          {...(context?.toolProgress !== undefined ? { progress: context.toolProgress } : {})}
        />
      )
    case 'notification':
      return renderNotificationBlocks(flattenNotificationEntries(row.thread.entries))
    case 'divider':
      return <ResultDivider model={row.divider} />
    case 'assistant-text':
      return <MarkdownText text={row.text} {...(context !== undefined ? { context } : {})} />
    case 'assistant-thinking':
      return <ThinkingMessage text={row.text} {...(context !== undefined ? { context } : {})} />
    case 'assistant-plan':
      return <MarkdownPlanLayout toolName="Plan" title="Proposed Plan" planText={row.text} {...(context !== undefined ? { context } : {})} />
    case 'user':
      return <UserContentMessage text={row.text} attachments={row.attachments} {...(context !== undefined ? { context } : {})} />
    case 'agent-prompt':
      return <AgentPromptView prompt={row.prompt} {...(context !== undefined ? { context } : {})} />
    case 'plan-execution':
      return <PlanExecutionMessage text={row.text} {...(context !== undefined ? { context } : {})} />
    case 'compact-summary':
      return <MarkdownText text={row.summary} {...(context !== undefined ? { context } : {})} />
    case 'control-response':
      return renderControlResponseRow(row.display, context)
    case 'hidden':
      return null
    default:
      return assertNever(row)
  }
}

/**
 * The subagent prompt card, shared by every provider that sends one.
 *
 * Promoted from Claude's local copy. Three surfaces draw this row -- a provider's own
 * prompt row, Claude's `agent_prompt`, and a prompt delivered into a child transcript
 * -- and the card must read the same for all three.
 */
export function AgentPromptView(props: { prompt: AgentPrompt, context?: ExpandableToolLayoutContext }): JSX.Element {
  // Key from the shared classification mapper (context.expandUiKey) so it matches
  // the estimator's pre-mount assumption; the literal is the context-less fallback.
  // untrack: the key is stable for a row (kind+provider don't change), so read it
  // once -- mirrors ThinkingBubble's `untrack(() => props.stateKey)`.
  const stateKey = untrack(() => props.context?.expandUiKey ?? MESSAGE_UI_KEY.AGENT_PROMPT)
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, stateKey)
  const text = () => props.prompt.prompt
  const isCollapsed = () => !expanded() && textNeedsCollapse(text())
  // The title states WHAT the subagent was asked to do when the provider said so.
  // `Prompt` alone is what every provider fell back to, and it named nothing.
  const title = () => [props.prompt.description, props.prompt.agentType].filter(Boolean).join(' · ') || 'Prompt'

  return (
    <ToolUseLayout
      icon={MessageSquare}
      toolName="Prompt"
      title={title()}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
      {...(props.context !== undefined ? { context: props.context } : {})}
    >
      <CollapsibleContent
        kind={props.prompt.promptFormat === 'pre' ? 'pre' : 'markdown-tool-result'}
        text={text()}
        isCollapsed={isCollapsed()}
        {...(props.context !== undefined ? { context: props.context } : {})}
      />
    </ToolUseLayout>
  )
}

/**
 * Overlay `completionHeader: true` on a render context without freezing its
 * reactive getters.
 *
 * Every own member of the base is re-stated on the overlay as a FORWARDING
 * descriptor -- a getter reads through to the base on every access, a value is
 * copied once -- and each stays OWN and enumerable. A plain `{...context, …}`
 * overlay evaluates every getter at one pass, so a row that streamed drew the
 * output it held when the interrupted header was built; a prototype overlay keeps
 * the getters live but leaves them off any later spread. This does both.
 */
function withCompletionHeader(context: RowRenderContext | undefined): RowRenderContext {
  const overlay: RowRenderContext = { completionHeader: true }
  if (context === undefined)
    return overlay
  for (const [key, descriptor] of Object.entries(Object.getOwnPropertyDescriptors(context))) {
    if (key === 'completionHeader')
      continue
    if (descriptor.get !== undefined || descriptor.set !== undefined) {
      Object.defineProperty(overlay, key, {
        ...(descriptor.get !== undefined ? { get: descriptor.get } : {}),
        ...(descriptor.set !== undefined ? { set: descriptor.set } : {}),
        enumerable: true,
        configurable: true,
      })
    }
    else {
      Object.defineProperty(overlay, key, { value: descriptor.value, writable: true, enumerable: true, configurable: true })
    }
  }
  return overlay
}

/**
 * Draw an extracted row, with the completion chrome every row shares.
 *
 * A row the provider could not read at all draws the shared unrecognized card, so the
 * reader still gets the frame. The interruption and failure headers wrap the drawn row
 * exactly as they wrap a legacy one, because they are LeapMux's own statement about
 * the row rather than any provider's.
 *
 * The completion comes from the EXTRACTION, which read LeapMux's own column and the
 * assembled envelope's own statement in that order. This function derived it from two
 * of its arguments instead, and the scroll rail derived it from one -- so an
 * interrupted thought whose worker column was unset carried its notice in the
 * transcript and lost it under the dot that jumps there.
 */
export function renderExtractedRow(
  extraction: ChatRowExtraction,
  context: RowRenderContext | undefined,
  messageMetadata?: unknown,
): JSX.Element {
  const row = extraction.kind === 'row' ? extraction.row : null
  // The one row kind the chrome below asks about. Held as its own narrowed binding so
  // the two questions -- does this row take a completion header, and does the outcome
  // note replace its body -- read the role off the same object.
  const toolRow = row?.kind === 'tool' ? row : null
  const completion = extraction.completion
  const toolCompletion = toolRow !== null && (completion === 'interrupted' || completion === 'error')
  // The outcome note is LeapMux's own statement about a tool row, so it is drawn here
  // rather than by any provider.
  const note = toolOutcomeNote(messageMetadata)
  // The note says the agent sent NO result for this call, and LeapMux concluded the
  // outcome itself. The RESULT row then draws no body at all: an empty body reads as
  // "the tool returned nothing", which asserts something the agent never reported.
  // ONE rule for every provider -- ZCode alone used to apply it, inside its own
  // renderer, so the same row on another provider drew "[no output]" beside the note.
  //
  // A suppression FLAG rather than an early return: returning here also skipped the
  // interruption header and the completion marker below, so a row LeapMux marked
  // interrupted drew one bare sentence with nothing saying which tool it belonged to
  // or that the turn had been stopped.
  const bodySuppressed = note !== null && toolRow?.role === 'result'
  const rowContext = toolCompletion
    ? withCompletionHeader(context)
    : context
  // A frame nobody could read still reaches the reader, in the card that says so.
  // The card states WHICH of the two happened: "LeapMux could not render this row"
  // for an extraction that threw, and "LeapMux has no display for this row" for one
  // no reader claimed. Folding them together blamed the provider for a defect in
  // LeapMux. The extraction outcome carries this distinction outside the row model.
  //
  // Built under the `if` rather than as a suppressed value, because a JSX expression
  // CALLS its component: an eagerly built body would run the tool renderer for a row
  // whose body the outcome note replaces.
  let drawn: JSX.Element = null
  if (!bodySuppressed) {
    drawn = extraction.kind === 'row'
      ? renderRowContent(extraction.row, rowContext)
      : <UnrecognizedMessage payload={extraction.payload} renderFailed={extraction.kind === 'failed'} {...(context !== undefined ? { context } : {})} />
  }
  const withNote = note === null
    ? drawn
    : (
        <>
          {drawn}
          <div role="note">{note}</div>
        </>
      )
  if (toolCompletion) {
    return (
      <ToolStatusHeader icon={CircleAlert} title={toolOutcomeLabel(completion === 'interrupted' ? 'interrupted' : 'failed')} dataToolMessage>
        {withNote}
      </ToolStatusHeader>
    )
  }
  const marker = completionMarker(completion)
  return marker
    ? (
        <>
          {withNote}
          <div role="note">{marker}</div>
        </>
      )
    : withNote
}
