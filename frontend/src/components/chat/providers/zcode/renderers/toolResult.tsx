import type { Component, JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { createMemo, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { CommandResultBody } from '../../../results/commandResult'
import { FileEditDiffBody } from '../../../results/fileEditDiff'
import { ReadFileResultBody } from '../../../results/readFileResult'
import { toolResultContentPre } from '../../../toolStyles.css'
import { extractZCodeBash, zcodeBashToCommandSource } from '../extractors/bash'
import { extractZCodeFileDiff, extractZCodeRead } from '../extractors/fileEdit'
import { zcodeErrorText, zcodeExtractTool, zcodeRowFrom } from '../extractors/toolCommon'

interface ResultProps {
  parsed: unknown
  context?: RenderContext
}
type ZCodeResultRenderer = Component<ResultProps>

function ZCodeBashResult(props: ResultProps): JSX.Element {
  const bash = createMemo(() =>
    extractZCodeBash(zcodeRowFrom(props)))
  return (
    <Show when={bash()}>
      {b => <CommandResultBody source={zcodeBashToCommandSource(b())} context={props.context} />}
    </Show>
  )
}

function ZCodeReadResult(props: ResultProps): JSX.Element {
  const read = createMemo(() =>
    extractZCodeRead(zcodeRowFrom(props)))
  return (
    <Show when={read()}>
      {r => <ReadFileResultBody source={r().source} context={props.context} />}
    </Show>
  )
}

/**
 * The generic result body: the text the model received, in a preformatted block.
 *
 * A FAILED call shows its error text instead, which the app-server states either on
 * an `error` object or as the result content.
 */
function ZCodeGenericResult(props: ResultProps): JSX.Element {
  const text = createMemo(() => {
    const update = zcodeExtractTool(props.parsed)
    if (!update)
      return ''
    return update.isError ? zcodeErrorText(update) : (update.result?.content ?? '')
  })
  return (
    <Show when={text()}>
      <pre class={toolResultContentPre}>{text()}</pre>
    </Show>
  )
}

/**
 * An edit/write result: the app-server's own structured patch when it sent one, else
 * the diff its input implies. A failed call falls back to the error text rather than
 * drawing the attempted edit as though it had landed.
 */
function ZCodeDiffResult(props: ResultProps): JSX.Element {
  const source = createMemo(() =>
    extractZCodeFileDiff(zcodeRowFrom(props)))
  return (
    <Show when={source()} fallback={<ZCodeGenericResult parsed={props.parsed} context={props.context} />}>
      {s => (
        <FileEditDiffBody
          source={s()}
          view={props.context?.diffView?.() ?? 'unified'}
          context={props.context}
        />
      )}
    </Show>
  )
}

const TOOL_RESULT_RENDERERS: Record<string, ZCodeResultRenderer> = {
  [ZCODE_TOOL.Bash]: ZCodeBashResult,
  [ZCODE_TOOL.Read]: ZCodeReadResult,
  [ZCODE_TOOL.Edit]: ZCodeDiffResult,
  [ZCODE_TOOL.Write]: ZCodeDiffResult,
}

export function ZCodeToolResultRenderer(props: ResultProps): JSX.Element {
  // A result payload carries no tool name of its own, so the span type (which the worker
  // sets from the tool name) and the paired scheduled row resolve it.
  const toolName = createMemo(() =>
    zcodeRowFrom(props).toolName)
  return (
    <Dynamic
      component={TOOL_RESULT_RENDERERS[toolName()] ?? ZCodeGenericResult}
      parsed={props.parsed}
      context={props.context}
    />
  )
}
