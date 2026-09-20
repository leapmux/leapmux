import type { JSX } from 'solid-js'
import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ToolRequestByKind, ToolResultByKind } from '~/components/chat/model/tools'
import { render } from '@solidjs/testing-library'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { rendererFor } from '~/components/chat/results/tools/index'
import { toolCallMeta } from '~/components/chat/results/tools/meta'
import { parsedCall } from '~/components/chat/results/tools/renderer'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'

/** What one kind module states about itself, for ONE kind. */
interface KindModuleCheckOf<K extends ToolKind> {
  kind: K
  /** A request that exercises the kind's typed title line. */
  request: ToolRequestByKind[K]
  /** Text the composed title must contain. */
  titlePart: string
  /** The result body's marker text, or a function stating it. */
  result: ToolResultByKind[K]
  /** Text the result body must draw. */
  resultPart?: string
  /** A long result of the same shape, for the collapsible answer. */
  longResult?: ToolResultByKind[K]
  /**
   * What the title says when the request is the minimal one. Kinds whose typed
   * request always composes a title (a list of '.', a glob of '*') state that
   * title here; the rest fall back to the call's own `RAW TITLE`.
   */
  minimalTitlePart?: string
}

/**
 * One kind module's check, as the DISTRIBUTED union over every kind.
 *
 * A union rather than a type parameter, and the difference is what removes an
 * assertion this harness used to carry. A generic body typechecks against an
 * ABSTRACT `K`, and `ToolCall<K>` is not assignable to `ToolCall` while `K`
 * stays abstract -- so every mounted row here needed `as ToolCall`, which is the
 * one form that can re-pair a kind with another kind's request. The union states the
 * correlation at the site that writes it instead: a case whose `kind` is `'read'` and
 * whose `request` is an `edit` request matches no member, and the call site fails to
 * compile. Inside, `check.kind` is the whole `ToolKind`, so `toolCallFixture` answers
 * `ToolCall` on its own.
 */
export type KindModuleCheck = { [K in ToolKind]: KindModuleCheckOf<K> }[ToolKind]

/**
 * The minimum every kind module states about itself, run the same way for each:
 * the title from a full request, the fallback to the call's own title, a body on
 * a result row, and a meta that answers for a short and a long result.
 */
export function checkKindModule(check: KindModuleCheck): void {
  const renderer = () => rendererFor({ kind: check.kind })

  // A call that has NOT answered, because the REQUEST is what titles that row. A
  // finished call words its header from the result -- the changes that landed, the
  // endpoint's own words -- so a completed one here asked the wrong side, and the
  // `RAW TITLE` beside it is what the request's title must beat.
  it('titles the row from a full request', () => {
    const call = toolCallFixture(check.kind, { status: 'in_progress', request: check.request, title: 'RAW TITLE' })
    expect(titleText(renderer().title(parsedCall(call), undefined))).toContain(check.titlePart)
  })

  it('falls back to the call\'s own title when the request states nothing', () => {
    const call = toolCallFixture(check.kind, { title: 'RAW TITLE' })
    expect(titleText(renderer().title(parsedCall(call), undefined))).toContain(check.minimalTitlePart ?? 'RAW TITLE')
  })

  it('draws the result body on a result row', () => {
    const call = toolCallFixture(check.kind, { result: check.result })
    const { container } = render(() => <ToolMessage row={toolRow(call)} />)
    expect(container.querySelector('[data-tool-message]')).not.toBeNull()
    if (check.resultPart !== undefined)
      expect(container.textContent).toContain(check.resultPart)
  })

  it('answers meta for a short result', () => {
    const meta = toolCallMeta(toolRow(toolCallFixture(check.kind, { result: check.result })))
    expect(meta.hasCopyable).toBe(meta.copyableContent() !== null)
  })

  it('answers meta for a long result', () => {
    const result = check.longResult ?? longVariant(check.result)
    const meta = toolCallMeta(toolRow(toolCallFixture(check.kind, { result })))
    expect(meta.hasCopyable).toBe(meta.copyableContent() !== null)
  })
}

/** Flatten a title element to the text a reader sees. */
function titleText(title: JSX.Element | string): string {
  if (typeof title === 'string')
    return title
  const { container } = render(() => <div>{title}</div>)
  return container.textContent ?? ''
}

/** How many entries a lengthened list holds. Past every collapse threshold in the kind table. */
const LONG_LIST_LENGTH = 40

/** Deepen one result into a long variant, when the kind states no explicit one. */
function longVariant<K extends ToolKind>(result: ToolResultByKind[K]): ToolResultByKind[K] {
  const long = Array.from({ length: 40 }, (_, index) => `line ${index}`).join('\n')
  const walk = (value: unknown): unknown => {
    if (typeof value === 'string')
      return value || long
    // A LONGER list, cycled from the entries the kind stated. Both branches used to
    // answer `value` itself, so the long-result case asserted exactly what the
    // short-result case asserted -- and the collapsible boundary went unchecked for
    // every list-shaped kind in the table. An EMPTY list has nothing to cycle, and a
    // kind whose result is a list of nothing states a `longResult` of its own.
    if (Array.isArray(value)) {
      return value.length === 0
        ? value
        : Array.from({ length: LONG_LIST_LENGTH }, (_, index) => walk(value[index % value.length]))
    }
    if (value && typeof value === 'object') {
      const out: Record<string, unknown> = {}
      for (const [key, entry] of Object.entries(value))
        out[key] = key === 'text' || key === 'output' || key === 'result' || key === 'summary' || key === 'content' || key === 'fallbackContent' || key === 'body' ? (entry || long) : walk(entry)
      return out
    }
    return value
  }
  return walk(result) as ToolResultByKind[K]
}
