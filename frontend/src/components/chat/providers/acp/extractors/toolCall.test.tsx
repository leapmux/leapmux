import type { ToolCall } from '../../../model/toolCall'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { toolRow } from '~/test-support/toolCallFixture'
import { ToolMessage } from '../../../results/ToolMessage'
import { cursorToolCallAdapter } from '../../cursor/extractors/toolCall'
import { reasonixToolCallAdapter } from '../../reasonix/extractors/toolCall'
import { acpToolCall } from './toolCall'

/**
 * The shared ACP build answers every kind with the request shape that kind DECLARES.
 *
 * The renderers read those fields without a guard -- `move` reads
 * `request.changes[0]`, `glob` reads `request.paths[0]` -- so a kind that came back
 * with the raw arguments instead threw out of the title getter and `MessageBubble`
 * replaced the whole message with "Failed to render message:". Both cases below are
 * rows a reader sees for the WHOLE time the call runs, which is why neither was
 * caught by a test that only built a completed call.
 */
function renderCall(call: ToolCall) {
  return render(() => <ToolMessage row={toolRow(call, 'request', { result: false })} />)
}

describe('acp shared default request', () => {
  it('names both files of a running reasonix move_file', () => {
    const call = acpToolCall({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'move-1',
      // Still RUNNING, which is the state the row spends its whole life in: the
      // shared default is what states the two files until the call completes.
      status: 'in_progress',
      kind: 'other',
      title: 'move_file',
      rawInput: { source_path: 'old/name.ts', destination_path: 'new/name.ts' },
    }, reasonixToolCallAdapter, undefined)

    expect(call.kind).toBe('move')
    expect(call.kind === 'move' && call.request.changes).toEqual([
      { filePath: 'new/name.ts', previousPath: 'old/name.ts', operation: 'move', oldStr: '', newStr: '', structuredPatch: null },
    ])
    const { container } = renderCall(call)
    expect(container.textContent).not.toContain('Failed to render message')
    expect(container.textContent).toContain('old/name.ts')
    expect(container.textContent).toContain('new/name.ts')
  })

  it('draws a pending cursor search instead of throwing', () => {
    const call = acpToolCall({
      sessionUpdate: 'tool_call',
      toolCallId: 'find-1',
      // `cursorSearchKind` reads the TITLE, which states the shape before the call
      // finishes, so the kind becomes `grep` while the wire kind is still `search`
      // and no adapter branch repaired the request yet.
      status: 'pending',
      kind: 'search',
      title: 'grep -l "needle"',
      rawInput: { pattern: 'needle' },
    }, cursorToolCallAdapter, undefined)

    expect(call.kind).toBe('grep')
    expect(call.kind === 'grep' && Array.isArray(call.request.paths)).toBe(true)
    const { container } = renderCall(call)
    expect(container.textContent).not.toContain('Failed to render message')
  })

  it('states each bare kind the declared request shape rather than the raw arguments', () => {
    // The two file-change kinds state the FILE they act on, and every other kind here
    // states no argument at all. A file operation that names no file is not a call this
    // build draws (invariant I7): the row degrades to the uncategorized card, and the
    // case would then ask its question of a kind it never meant to reach.
    const shapes: Array<[string, Record<string, unknown>, (request: Record<string, unknown>) => boolean]> = [
      ['delete', { path: '/p/gone.ts' }, request => Array.isArray(request.changes)],
      ['move', { source_path: '/p/a.ts', destination_path: '/p/b.ts' }, request => Array.isArray(request.changes)],
      ['glob', {}, request => Array.isArray(request.paths) && typeof request.pattern === 'string'],
      ['grep', {}, request => Array.isArray(request.paths) && typeof request.pattern === 'string'],
      ['todo', {}, request => Array.isArray(request.items)],
      ['question', {}, request => Array.isArray(request.questions)],
      ['think', {}, request => typeof request.text === 'string'],
      ['list', {}, request => typeof request.path === 'string'],
      ['web_search', {}, request => typeof request.query === 'string'],
      ['message', {}, request => typeof request.text === 'string'],
      ['chart', {}, request => typeof request.spec === 'string'],
      ['trigger', {}, request => typeof request.action === 'string'],
    ]
    for (const [kind, rawInput, holds] of shapes) {
      const call = acpToolCall({
        sessionUpdate: 'tool_call',
        toolCallId: `bare-${kind}`,
        status: 'pending',
        kind,
        rawInput,
      }, undefined, undefined)
      expect(call.kind, `${kind} must keep its own kind`).toBe(kind)
      expect(holds(call.request as Record<string, unknown>), `${kind} must state its declared request`).toBe(true)
    }
  })
})
