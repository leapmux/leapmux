import type { JSX } from 'solid-js'
import type { ToolMetadataEntry } from '../../model/toolMetadata'
import type { CommandAction, CommandLanguage, ExecuteRequest } from '../../model/tools/execute'
import type { ToolResultRenderContext } from '../../renderContext'
import type { ParsedCall, ToolKindRenderer, ToolRowView } from './renderer'
import Terminal from 'lucide-solid/icons/terminal'
import { For, Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { relativizePath } from '~/lib/paths'
import { commandInputNeedsExpansion } from '../../chatHeightShared'
import { commandActionCodeText, commandActionLine, commandActionList, commandActionSingle, commandInputCollapsed, commandInputCollapsedFade, toolInputText } from '../../toolStyles.css'
import { commandOutputIsCollapsible, CommandResultBody, CommandResultList } from '../commandResult'
import { CommandInputSummary, useCollapsedSummaryOverflow } from '../multiLineCommandBody'
import { ToolMetadata } from '../ToolMetadata'
import { ToolHeaderRow } from '../ToolStatusHeader'

/** A description long enough to crowd the header is clipped, with the cut marked. */
const DESCRIPTION_LIMIT = 100

function actionPath(path: string, context: ToolResultRenderContext | undefined): string {
  return relativizePath(path, context?.workingDir, context?.homeDir)
}

function commandActionDescription(action: Exclude<CommandAction, { kind: 'unknown' }>, context: ToolResultRenderContext | undefined): JSX.Element {
  switch (action.kind) {
    case 'read': {
      const path = actionPath(action.path, context) || action.name
      return (
        <>
          <span>Read </span>
          <span class={commandActionCodeText}>{path}</span>
        </>
      )
    }
    case 'list': {
      const path = action.path ? actionPath(action.path, context) : ''
      return (
        <>
          <span>{path ? 'List files in ' : 'List files'}</span>
          {path ? <span class={commandActionCodeText}>{path}</span> : null}
        </>
      )
    }
    case 'search': {
      const path = action.path ? actionPath(action.path, context) : ''
      return (
        <>
          <span>{action.query ? 'Search for ' : 'Search'}</span>
          {action.query ? <span class={commandActionCodeText}>{`"${action.query}"`}</span> : null}
          {path ? <span> in </span> : null}
          {path ? <span class={commandActionCodeText}>{path}</span> : null}
        </>
      )
    }
  }
}

function KnownCommandAction(props: {
  action: Exclude<CommandAction, { kind: 'unknown' }>
  language: CommandLanguage | undefined
  context: ToolResultRenderContext | undefined
  title?: boolean
}): JSX.Element {
  return (
    <Tooltip
      text={props.action.command}
      content={(
        <CommandInputSummary
          command={props.action.command}
          {...(props.language !== undefined ? { language: props.language } : {})}
          {...(props.context !== undefined ? { context: props.context } : {})}
        />
      )}
    >
      <span
        class={props.title ? `${toolInputText} ${commandActionLine}` : commandActionLine}
        data-command-action={props.title ? undefined : props.action.kind}
        data-testid={props.title ? 'execute-title' : undefined}
      >
        {commandActionDescription(props.action, props.context)}
      </span>
    </Tooltip>
  )
}

function commandActionEntry(action: CommandAction, language: CommandLanguage | undefined, view: ToolRowView): JSX.Element {
  if (action.kind === 'unknown') {
    return (
      <div data-command-action={action.kind}>
        <CommandInputSummary
          command={action.command}
          {...(language !== undefined ? { language } : {})}
          {...(view.context !== undefined ? { context: view.context } : {})}
        />
      </div>
    )
  }
  return <KnownCommandAction action={action} language={language} context={view.context} />
}

/** A single semantic action that can replace the generic execute title. */
function knownActionForTitle(call: ParsedCall<'execute'>): Exclude<CommandAction, { kind: 'unknown' }> | undefined {
  if (call.request.description || (call.title && call.title !== 'Run command'))
    return undefined
  const action = call.request.actions?.length === 1 ? call.request.actions[0] : undefined
  return action?.kind === 'unknown' ? undefined : action
}

function CommandActionSummary(props: {
  actions: CommandAction[]
  language?: CommandLanguage
  view: ToolRowView
}): JSX.Element {
  const collapsed = () => !props.view.expanded()
  const overflow = useCollapsedSummaryOverflow({
    collapsed,
    content: () => props.actions,
    onOverflowChange: value => props.view.onSummaryOverflow(value),
  })
  const summaryClass = (base: string): string => [
    base,
    collapsed() && commandInputCollapsed,
    collapsed() && overflow.overflowing() && commandInputCollapsedFade,
  ].filter(Boolean).join(' ')
  const singleAction = () => props.actions.length === 1 ? props.actions[0] : undefined
  return (
    <Show
      when={singleAction()}
      fallback={(
        <ul ref={overflow.elementRef} class={summaryClass(commandActionList)}>
          <For each={props.actions}>
            {action => <li>{commandActionEntry(action, props.language, props.view)}</li>}
          </For>
        </ul>
      )}
    >
      {action => (
        <div ref={overflow.elementRef} class={summaryClass(commandActionSingle)}>
          {commandActionEntry(action(), props.language, props.view)}
        </div>
      )}
    </Show>
  )
}

function commandMetadata(request: ExecuteRequest, workingDir?: string, homeDir?: string): ToolMetadataEntry[] {
  return [
    ...(request.cwd ? [{ label: 'Working directory', value: relativizePath(request.cwd, workingDir, homeDir) }] : []),
    ...(request.processId ? [{ label: 'Process ID', value: request.processId }] : []),
  ]
}

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
   * A description wins first. A specific frame title wins next. A generic
   * `Run command` frame with one known action uses that action's semantic
   * description, with the raw command in its Tooltip. An unknown action keeps
   * the generic title and raw-command body. The generic singular or plural
   * title is the last fallback.
   */
  title(call, context) {
    const description = call.request.description
    const clipped = description !== undefined && description.length > DESCRIPTION_LIMIT
      ? `${description.slice(0, DESCRIPTION_LIMIT)}…`
      : description
    const frameTitle = call.title === 'Run command' ? undefined : call.title
    const knownAction = knownActionForTitle(call)
    const defaultTitle = (call.request.actions?.length ?? 0) > 1 ? 'Run commands' : 'Run command'
    // ONE classed span over every branch. `ToolUseLayout` wraps a STRING title in
    // `toolInputText` itself and leaves a JSX title alone, so the bare fragment this
    // used to answer reached the header with no class at all: no monospace face, and
    // none of the one-line clip, which let an unbreakable title wrap the header onto
    // extra rows.
    return (
      <Show
        when={knownAction}
        fallback={<span class={toolInputText} data-testid="execute-title">{clipped || frameTitle || defaultTitle}</span>}
      >
        {action => (
          <KnownCommandAction
            action={action()}
            language={call.request.language}
            context={context}
            title
          />
        )}
      </Show>
    )
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
        <Show when={knownActionForTitle(call) === undefined}>
          <Show
            when={call.request.actions?.length ? call.request.actions : undefined}
            fallback={<CommandInputSummary command={call.request.command} {...(call.request.language !== undefined ? { language: call.request.language } : {})} {...(view.context !== undefined ? { context: view.context } : {})} collapsed={!view.expanded()} onOverflowChange={view.onSummaryOverflow} />}
          >
            {actions => (
              <CommandActionSummary
                actions={actions()}
                {...(call.request.language !== undefined ? { language: call.request.language } : {})}
                view={view}
              />
            )}
          </Show>
        </Show>
        <ToolMetadata items={commandMetadata(call.request, view.context?.workingDir, view.context?.homeDir)} />
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
    const hasActions = (call.request.actions?.length ?? 0) > 0
    return {
      collapsible: !hasActions && commandInputNeedsExpansion(call.request.command),
      expandLabel: hasActions ? 'Show all actions' : 'Show full command',
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
