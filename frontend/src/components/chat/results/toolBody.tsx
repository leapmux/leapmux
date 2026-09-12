import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { ToolBodySource } from './toolPresentation'
import { For, Show } from 'solid-js'
import { TodoListBody } from '../todoListMessage'
import { toolInputSummary } from '../toolStyles.css'
import { AgentResultBody } from './agentResult'
import { CollapsibleContent } from './CollapsibleContent'
import { CommandResultBody, CommandResultList } from './commandResult'
import { DirectoryResultBody } from './directoryResult'
import { FileEditDiffBody, FileEditDiffTitle, fileEditHasDiff } from './fileEditDiff'
import { McpToolCallBody } from './mcpToolCall'
import { ReadFileResultBody } from './readFileResult'
import { SearchResultBody } from './searchResult'
import { WebFetchResultBody } from './webFetchResult'

/** Render the body that the provider describes through shared tool sources. */
export function renderToolBody(body: ToolBodySource, context: RenderContext): JSX.Element | null {
  switch (body.type) {
    case 'agent': return <AgentResultBody source={body.source} context={context} />
    case 'command': return <CommandResultBody source={body.source} context={context} />
    case 'commands': return <CommandResultList entries={body.entries} context={context} />
    case 'directory': return <DirectoryResultBody source={body.source} context={context} />
    case 'diff': return (
      <For each={body.sources}>
        {source => (
          <>
            <Show when={body.sources.length > 1}>
              <div class={toolInputSummary}><FileEditDiffTitle source={source} context={context} /></div>
            </Show>
            <Show when={fileEditHasDiff(source)}><FileEditDiffBody source={source} view={context.diffView?.() ?? 'unified'} context={context} /></Show>
          </>
        )}
      </For>
    )
    case 'read': return <ReadFileResultBody source={body.source} context={context} />
    case 'search': return <SearchResultBody source={body.source} context={context} />
    case 'fetch': return <WebFetchResultBody source={body.source} context={context} />
    case 'mcp': return <McpToolCallBody source={body.source} context={context} />
    case 'todo': return <TodoListBody todos={body.items} />
    case 'markdown': return <CollapsibleContent kind="markdown-tool-result" text={body.text} isCollapsed={false} context={context} />
    case 'text': return null
  }
}
