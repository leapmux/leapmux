import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import ListTodo from 'lucide-solid/icons/list-todo'
import { createMemo, Show } from 'solid-js'
import { MarkdownText } from '../../../messageRenderers'
import { ImageResultList } from '../../../results/imageResult'
import { McpToolMessage } from '../../../results/McpToolMessage'
import { ToolMetadata } from '../../../results/ToolMetadata'
import { TodoListBody } from '../../../todoListMessage'
import { ToolResultMessage } from '../../../toolRenderers'
import { ToolMessageLayout } from '../../../widgets/ToolMessageLayout'
import { piGenericToolSource } from '../extractors/generic'
import { piToolResultImages } from '../extractors/image'
import { piTodoSource } from '../extractors/todo'
import { piPairedRequest } from '../extractors/toolCommon'

interface Props { payload: Record<string, unknown>, context?: RenderContext }

function PiTodoMessage(props: Props & { role: 'request' | 'result' }): JSX.Element {
  const source = createMemo(() => piTodoSource(props.payload, props.context?.sources?.request(), props.context?.sources?.result()))
  const generic = createMemo(() => !source() ? piGenericToolSource(props.payload, props.context?.sources?.request()) : null)
  const hasRequest = () => !!piPairedRequest(props.payload, props.context?.sources?.request())
  const images = createMemo(() => props.role === 'result' && source() ? piToolResultImages(props.payload, undefined, props.context?.sources?.request()) : [])
  return (
    <Show when={source()} fallback={<Show when={generic()}>{value => <McpToolMessage source={value()} role={props.role} hasRequest={hasRequest()} context={props.context} />}</Show>}>
      {value => (
        <ToolMessageLayout role={props.role} hasRequest={hasRequest()} icon={ListTodo} toolName={value().list.toolName} title={value().list.title} alwaysVisible context={props.context}>
          <Show when={props.role === 'result'}>
            <Show when={!value().error} fallback={<ToolResultMessage resultContent={value().error ?? ''} isError context={props.context} />}>
              <TodoListBody todos={value().list.todos} emptyText={value().list.emptyText} />
              <Show when={value().description}><MarkdownText text={value().description} context={props.context} /></Show>
              <ToolMetadata items={value().metadata} />
            </Show>
            <Show when={images().length}><ImageResultList sources={images()} title={value().list.title} context={props.context} /></Show>
          </Show>
        </ToolMessageLayout>
      )}
    </Show>
  )
}

export function PiTodoRequest(props: Props): JSX.Element {
  return <PiTodoMessage {...props} role="request" />
}

export function PiTodoResult(props: Props): JSX.Element {
  return <PiTodoMessage {...props} role="result" />
}
