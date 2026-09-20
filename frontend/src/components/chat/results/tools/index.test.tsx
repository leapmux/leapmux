import type { ToolCall } from '~/components/chat/model/toolCall'
import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { failedResult, unparsedResult } from '~/components/chat/model/toolCall'
import { TOOL_KINDS } from '~/components/chat/model/toolKind'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { toolCallDisplayName } from '~/components/chat/results/tools/header'
import { rendererFor, TOOL_KIND_RENDERERS } from '~/components/chat/results/tools/index'
import { toolCallMeta } from '~/components/chat/results/tools/meta'
import { parsedCall } from '~/components/chat/results/tools/renderer'
import { MINIMAL_REQUEST, MINIMAL_RESULT, toolCallFixture, toolRow } from '~/test-support/toolCallFixture'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

describe('the tool kind renderer table', () => {
  it.each(TOOL_KINDS)('holds a renderer for %j whose label starts a capital word', (kind) => {
    const renderer = TOOL_KIND_RENDERERS[kind]
    expect(renderer.label).toMatch(/^[A-Z]/)
    expect(renderer.icon).toBeDefined()
  })

  it.each(TOOL_KINDS)('renders %j as a request row, a result row, and both fallbacks, without throwing', (kind) => {
    for (const call of [
      toolCallFixture(kind, { status: 'in_progress' }),
      toolCallFixture(kind, { result: MINIMAL_RESULT[kind] }),
      toolCallFixture(kind, { result: unparsedResult('raw'), title: 'RAW TITLE' }),
      toolCallFixture(kind, { result: failedResult('boom'), status: 'failed', title: 'RAW TITLE' }),
    ] as ToolCall[]) {
      const { container } = render(() => <ToolMessage row={toolRow(call)} />)
      expect(container.querySelector('[data-tool-message]')).not.toBeNull()
    }
  })

  it.each(TOOL_KINDS)('states the unparsed payload for %j', (kind) => {
    const call = toolCallFixture(kind, { result: unparsedResult('raw'), title: 'RAW TITLE' }) as ToolCall
    const { container } = render(() => <ToolMessage row={toolRow(call)} />)
    expect(container.textContent).toContain('raw')
  })

  it.each(TOOL_KINDS)('answers hasCopyable truthfully and titles the row for %j', (kind) => {
    const call = toolCallFixture(kind, { result: unparsedResult('raw'), title: 'RAW TITLE' }) as ToolCall
    const meta = toolCallMeta(toolRow(call))
    expect(meta.hasCopyable).toBe(meta.copyableContent() !== null)
    const renderer = rendererFor(call)
    const title = renderer.title(parsedCall(call), undefined)
    expect(typeof title === 'string' ? title.trim() : title).toBeTruthy()
  })

  it('lets the tool name lead for the nameless kinds alone', () => {
    // A kind with a category of its own draws the category's word beside the icon;
    // the nameless trio draws the humanized tool NAME, which identifies the tool.
    for (const kind of TOOL_KINDS) {
      const renderer = TOOL_KIND_RENDERERS[kind]
      const name = toolCallDisplayName(toolCallFixture(kind, { name: 'semantic_search', title: 'RAW TITLE' }) as ToolCall)
      if (kind === 'unspecified' || kind === 'other')
        expect(name).toBe('Semantic search')
      else if (kind === 'mcp')
        expect(name).toBe('s / t')
      else
        expect(name).toBe(renderer.label)
    }
  })

  it('keeps every minimal request and result keyed by the same kinds', () => {
    expect(Object.keys(MINIMAL_REQUEST).sort()).toEqual([...TOOL_KINDS].sort())
    expect(Object.keys(MINIMAL_RESULT).sort()).toEqual([...TOOL_KINDS].sort())
  })
})
