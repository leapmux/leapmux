import type { ToolKindRenderer } from './renderer'
import Terminal from 'lucide-solid/icons/terminal'
import { For, Show } from 'solid-js'
import { relativizePath } from '~/lib/paths'
import { commandInputNeedsExpansion } from '../../chatHeightShared'
import { commandOutputIsCollapsible } from '../../ir/commandResult'
import { toolInputSummary, toolInputText } from '../../toolStyles.css'
import { CommandResultBody, CommandResultList } from '../commandResult'
import { CommandInputSummary } from '../multiLineCommandBody'
import { ToolHeaderRow } from '../ToolStatusHeader'

/** A description long enough to crowd the header is clipped, with the cut marked. */
const DESCRIPTION_LIMIT = 100

export const executeRenderer: ToolKindRenderer<'execute'> = {
  icon: Terminal,
  label: 'Execute',
  // `CommandResultBody` states the outcome per command, so a call that returned no
  // command at all -- a failure the provider reported with nothing beside it -- draws
  // none, and the shared header is the only thing left that can.
  statesOwnOutcome: call => call.result.commands.length > 0,
  /**
   * What the command was FOR, in the words the agent sent.
   *
   * The COMMAND is never the title: the summary below states it, and a header that
   * repeated it would sit above the very line it copies. So a call whose provider
   * states no description falls to the frame's own title, and then to `Run command` --
   * which is the shape of the bug to look for here. Every provider that receives a
   * description must put it on `request.description`; `Run command` on a row that had
   * one means its extractor dropped it, not that this renderer refused it.
   */
  title(call) {
    const description = call.request.description
    const clipped = description !== undefined && description.length > DESCRIPTION_LIMIT
      ? `${description.slice(0, DESCRIPTION_LIMIT)}…`
      : description
    // ONE classed span over every branch. `ToolUseLayout` wraps a STRING title in
    // `toolInputText` itself and leaves a JSX title alone, so the bare fragment this
    // used to answer reached the header with no class at all: no monospace face, and
    // none of the one-line clip, which let an unbreakable title wrap the header onto
    // extra rows.
    return <span class={toolInputText}>{clipped || (call.title ?? 'Run command')}</span>
  },
  summary(call, view) {
    // The command belongs to the rows that state the REQUEST. A result row with
    // its request beside it draws only what the command answered -- the request
    // above states the command -- while a lone result row (a single-frame call)
    // is the only place the command is ever stated, so it keeps the summary. An
    // UPDATE row keeps it too: it may be the only row the call has.
    // Expanding un-clips the SAME summary the collapsed row shows, the way a
    // result's expand un-clips its output: one area, clipped to three rows or
    // full height, never a second component swapped in beside it.
    return (
      <Show when={!(view.role === 'result' && view.hasRequestRow)}>
        <CommandInputSummary command={call.request.command} {...(call.request.language !== undefined ? { language: call.request.language } : {})} {...(view.context !== undefined ? { context: view.context } : {})} collapsed={!view.expanded()} onOverflowChange={view.onSummaryOverflow} />
        {/* A command run outside the workspace root reads as though it ran at the
            root without this, which changes what its output means. */}
        <Show when={call.request.cwd}>
          {dir => <div class={toolInputSummary}>{`cwd: ${relativizePath(dir(), view.context?.workingDir, view.context?.homeDir)}`}</div>}
        </Show>
      </Show>
    )
  },
  result(call, view) {
    const commands = call.result.commands
    return (
      <>
        {/* The unresolved terminals belong to the RESULT. `ToolMessage` draws the
            request hook on every row of a span while it restricts the result to the
            rows where `drawsResult` is true, so stating them there printed
            "Terminal t-3" once above the command and again above its output for the
            same call. */}
        <For each={call.result.unresolvedTerminals ?? []}>{id => <ToolHeaderRow icon={Terminal} title={`Terminal ${id}`} />}</For>
        {/* The single-command gate doubles as the read: `commands[0]` is defined
            exactly when the count is one, and `Show` narrows the value it holds. */}
        <Show when={commands.length === 1 ? commands[0] : undefined} fallback={<CommandResultList entries={commands} status={call.status} {...(view.context !== undefined ? { context: view.context } : {})} />}>
          {command => <CommandResultBody source={command()} status={call.status} {...(view.context !== undefined ? { context: view.context } : {})} />}
        </Show>
      </>
    )
  },
  requestMeta(call) {
    return {
      collapsible: commandInputNeedsExpansion(call.request.command),
      expandLabel: 'Show full command',
      copyableContent: () => call.request.command || null,
      copyLabel: 'Copy Command',
    }
  },
  resultMeta(call) {
    return {
      collapsible: call.result.commands.some(command => commandOutputIsCollapsible(command)),
      hasDiff: false,
      copyableContent: () => call.result.commands.map(command => command.output).filter(Boolean).join('\n\n') || null,
    }
  },
}
