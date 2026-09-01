import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT, ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { ZCODE_DISPLAY } from '../protocol'
import { ZCodeAssistantMessage } from './assistantMessage'
import { ZCodeToolExecutionRenderer } from './toolExecution'
import { ZCodeToolResultRenderer } from './toolResult'

function toolEvent(kind: string, payload: Record<string, unknown> = {}): Record<string, unknown> {
  return { type: ZCODE_EVENT.ToolUpdated, payload: { kind, toolCallId: 'call-1', ...payload } }
}

function scheduled(toolName: string, input: Record<string, unknown> = {}) {
  return toolEvent(ZCODE_TOOL_KIND.Scheduled, { toolName, input })
}

function parsedOf(parent: Record<string, unknown>) {
  return { rawText: '', topLevel: parent, parentObject: parent, wrapper: null }
}

describe('zcode assistant renderer', () => {
  it('renders the model-response text', () => {
    const { container } = render(() => (
      <ZCodeAssistantMessage parsed={{
        type: ZCODE_EVENT.SessionUpdated,
        payload: { content: 'the answer', stopReason: 'stop' },
      }}
      />
    ))
    expect(container.textContent).toContain('the answer')
  })

  it('renders nothing for an empty model-response', () => {
    const { container } = render(() => (
      <ZCodeAssistantMessage parsed={{
        type: ZCODE_EVENT.SessionUpdated,
        payload: { content: '', stopReason: 'tool-calls' },
      }}
      />
    ))
    expect(container.textContent).toBe('')
  })
})

describe('zcode tool execution renderer', () => {
  it('renders a Bash command in the title', () => {
    const { container } = render(() => (
      <ZCodeToolExecutionRenderer parsed={scheduled(ZCODE_TOOL.Bash, { command: 'ls -la /tmp' })} />
    ))
    expect(container.textContent).toContain('ls -la /tmp')
  })

  it('renders a Read with the file path and, when present, the requested range', () => {
    const { container } = render(() => (
      <ZCodeToolExecutionRenderer parsed={scheduled(ZCODE_TOOL.Read, {
        file_path: '/tmp/a.ts',
        offset: 10,
        limit: 5,
      })}
      />
    ))
    expect(container.textContent).toContain('/tmp/a.ts')
    // `limit` is a line COUNT, so the inclusive last line is offset + limit - 1.
    expect(container.textContent).toContain('10-14')
  })

  it('renders a Write and an Edit with the file path', () => {
    const { container: write } = render(() => (
      <ZCodeToolExecutionRenderer parsed={scheduled(ZCODE_TOOL.Write, { file_path: '/tmp/new.ts' })} />
    ))
    expect(write.textContent).toContain('Write')
    expect(write.textContent).toContain('/tmp/new.ts')

    const { container: edit } = render(() => (
      <ZCodeToolExecutionRenderer parsed={scheduled(ZCODE_TOOL.Edit, { file_path: '/tmp/a.ts' })} />
    ))
    expect(edit.textContent).toContain('Edit')
    expect(edit.textContent).toContain('/tmp/a.ts')
  })

  it('renders a subagent spawn with its description and prompt', () => {
    const { container } = render(() => (
      <ZCodeToolExecutionRenderer parsed={scheduled(ZCODE_TOOL.Agent, {
        description: 'check tests',
        prompt: 'run the failing ones',
      })}
      />
    ))
    expect(container.textContent).toContain('Agent: check tests')
    expect(container.textContent).toContain('run the failing ones')
  })

  it('renders a Grep with its pattern', () => {
    const { container } = render(() => (
      <ZCodeToolExecutionRenderer parsed={scheduled(ZCODE_TOOL.Grep, { pattern: 'TODO' })} />
    ))
    expect(container.textContent).toContain('TODO')
  })

  // A tool with no dedicated renderer still opens a span, titled with its own name,
  // rather than falling through to a raw JSON bubble.
  it('renders an unknown tool by its name', () => {
    const { container } = render(() => (
      <ZCodeToolExecutionRenderer parsed={scheduled('SomeToolAddedLater', { arg: 1 })} />
    ))
    expect(container.textContent).toContain('SomeToolAddedLater')
  })
})

describe('zcode tool result renderer', () => {
  it('renders a Bash result through the shared command-result body', () => {
    const { container } = render(() => (
      <ZCodeToolResultRenderer
        parsed={toolEvent(ZCODE_TOOL_KIND.Result, {
          result: { content: 'total 48\nfile1', perf: { detail: { kind: 'command', command: { exitCode: 0 } } } },
        })}
        context={{ spanType: ZCODE_TOOL.Bash }}
      />
    ))
    expect(container.textContent).toContain('file1')
  })

  it('renders a numbered Read result as numbered lines', () => {
    const { container } = render(() => (
      <ZCodeToolResultRenderer
        parsed={toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: '1\talpha\n2\tbeta' } })}
        context={{ spanType: ZCODE_TOOL.Read }}
      />
    ))
    expect(container.textContent).toContain('alpha')
    expect(container.textContent).toContain('beta')
  })

  it('renders an Edit structured-patch result as a diff', () => {
    const { container } = render(() => (
      <ZCodeToolResultRenderer
        parsed={toolEvent(ZCODE_TOOL_KIND.Result, {
          result: {
            display: {
              kind: ZCODE_DISPLAY.FileDiff,
              filePath: '/tmp/a.ts',
              structuredPatch: [{
                oldStart: 1,
                oldLines: 1,
                newStart: 1,
                newLines: 1,
                lines: ['-oldMarker', '+newMarker'],
              }],
            },
          },
        })}
        context={{ spanType: ZCODE_TOOL.Edit }}
      />
    ))
    expect(container.textContent).toContain('newMarker')
  })

  // A failed edit renders its error text, not the attempted substitution.
  it('renders a failed Edit as the error text, not the attempted patch', () => {
    const { container } = render(() => (
      <ZCodeToolResultRenderer
        parsed={toolEvent(ZCODE_TOOL_KIND.Error, {
          error: { message: 'old_string not found' },
          result: {
            display: {
              kind: ZCODE_DISPLAY.FileDiff,
              filePath: '/tmp/a.ts',
              structuredPatch: [{
                oldStart: 1,
                oldLines: 1,
                newStart: 1,
                newLines: 1,
                lines: ['-wouldHaveBeenOld', '+wouldHaveBeenNew'],
              }],
            },
          },
        })}
        context={{
          spanType: ZCODE_TOOL.Edit,
          toolUseParsed: parsedOf(scheduled(ZCODE_TOOL.Edit, {
            file_path: '/tmp/a.ts',
            old_string: 'wouldHaveBeenOld',
            new_string: 'wouldHaveBeenNew',
          })),
        }}
      />
    ))
    expect(container.textContent).toContain('old_string not found')
    expect(container.textContent).not.toContain('wouldHaveBeenNew')
  })

  it('renders an unknown-tool result as the result content', () => {
    const { container } = render(() => (
      <ZCodeToolResultRenderer
        parsed={toolEvent(ZCODE_TOOL_KIND.Result, { result: { content: 'a/b.ts' } })}
        context={{ spanType: 'SomeToolAddedLater' }}
      />
    ))
    expect(container.textContent).toContain('a/b.ts')
  })
})

describe('zcode TodoWrite renderer', () => {
  it('draws the list as a checklist rather than as raw JSON', () => {
    const { container } = render(() => (
      <ZCodeToolExecutionRenderer parsed={scheduled(ZCODE_TOOL.TodoWrite, {
        todos: [
          { content: 'Write the parser', status: 'in_progress', activeForm: 'Writing the parser' },
          { content: 'Add tests', status: 'pending' },
        ],
      })}
      />
    ))
    expect(container.textContent).toContain('2 tasks')
    // A running row reads as its activeForm; a pending one reads as its content.
    expect(container.textContent).toContain('Writing the parser')
    expect(container.textContent).toContain('Add tests')
    expect(container.textContent).not.toContain('"status"')
  })

  it('shows the cleared state for an emptied list', () => {
    const { container } = render(() => (
      <ZCodeToolExecutionRenderer parsed={scheduled(ZCODE_TOOL.TodoWrite, { todos: [] })} />
    ))
    expect(container.textContent).toContain('cleared')
  })

  // An input with no todos array is not a to-do row; the generic tool card still
  // states which tool ran instead of drawing an empty list.
  it('falls back to the generic row when the input carries no todos', () => {
    const { container } = render(() => (
      <ZCodeToolExecutionRenderer parsed={scheduled(ZCODE_TOOL.TodoWrite, { note: 'nothing' })} />
    ))
    expect(container.textContent).toContain(ZCODE_TOOL.TodoWrite)
    expect(container.textContent).toContain('nothing')
  })
})
