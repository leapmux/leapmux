import type { JSX } from 'solid-js'
import type { ToolCall } from '../../model/toolCall'
import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT, COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { providerRowImages, providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import { renderMessageContent } from '../../messageContentRenderer'
import { rendererFor } from '../../results/tools'
import { parsedCall } from '../../results/tools/renderer'
import { toolBodyBorder, toolUseHeader } from '../../toolStyles.css'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'
import '../testMocks'

const CALL = 'copilot-tool'

/** One persisted native frame, exactly as the worker stores it. */
function frame(type: string, data: Record<string, unknown>): Record<string, unknown> {
  return {
    jsonrpc: '2.0',
    method: 'session.event',
    params: { sessionId: 'session-1', event: { id: `${type}-1`, type, data } },
  }
}

function parsed(row: Record<string, unknown>): ResolvedMessageContent {
  return { ...input(row, undefined, AgentProvider.GITHUB_COPILOT), supplementalContent: undefined }
}

function start(args: Record<string, unknown>, toolName: string, toolCallId = CALL) {
  return frame(COPILOT_EVENT.ToolStarted, { toolCallId, toolName, arguments: args })
}

function complete(data: Record<string, unknown>, toolCallId = CALL) {
  return frame(COPILOT_EVENT.ToolCompleted, { toolCallId, success: true, ...data })
}

const plugin = () => providerFor(AgentProvider.GITHUB_COPILOT)!

/** The words one call's kind composes for its header, as `ToolMessageLayout` draws them. */
function titleTextOf(call: ToolCall): string {
  const { container } = render(() => rendererFor(call).title(parsedCall(call), undefined) as JSX.Element)
  return container.textContent ?? ''
}

/** Render one row with the sources the store would supply for its pair. */
function renderRow(row: Record<string, unknown>, request?: Record<string, unknown>, spanType?: string, requestVisible = false) {
  const category = plugin().transcript.classify(input(row))
  const role = request === undefined ? plugin().transcript.spanRole?.(input(row)) ?? 'other' : 'result'
  const sources = testMessageSources({
    current: () => parsed(row),
    ...(request ? { request: () => parsed(request) } : {}),
    role: () => role,
    visibleRows: () => ({ request: requestVisible, result: role === 'result' }),
  })
  return render(() => renderMessageContent(row, { premeasureMode: true, ...(spanType !== undefined ? { spanType } : {}), sources }, category, AgentProvider.GITHUB_COPILOT))
}

/** Render the result half of one tool call, with its start row as the request. */
function renderResult(args: Record<string, unknown>, toolName: string, result: Record<string, unknown>, requestVisible = false) {
  const request = start(args, toolName)
  return renderRow(complete(result), request, toolName, requestVisible)
}

describe('copilot native tool rendering', () => {
  // The request card above a result already carries the header and the border, so the
  // result draws neither again. The ACP suite checked this for its own five providers
  // and once listed Copilot too, which was meaningless: Copilot reads no Agent Client
  // Protocol frame. This is that check in the shape Copilot actually sends.
  it('renders a paired read result without another header or body border', () => {
    const { container } = renderResult({ path: '/project/README.md' }, COPILOT_TOOL.View, {
      result: { content: 'File content' },
    }, true)
    expect(container.textContent).toContain('File content')
    expect(container.querySelector(`.${toolUseHeader}`)).toBeNull()
    expect(container.querySelector(`.${toolBodyBorder}`)).toBeNull()
  })

  it('recovers file lines from the native read details and copies the displayed content', () => {
    const args = { path: '/project/sample.txt', view_range: [2, 4] }
    const result = {
      result: {
        content: 'Shortened for the model',
        detailedContent: '\ndiff --git a/project/sample.txt b/project/sample.txt\nindex 0000000..0000000 100644\n--- a/project/sample.txt\n+++ b/project/sample.txt\n@@ -2,3 +2,3 @@\n second\n third\n 한글\n',
      },
    }
    const { container } = renderResult(args, COPILOT_TOOL.View, result)
    expect(container.textContent).toContain('second')
    expect(container.textContent).toContain('third')
    expect(container.textContent).toContain('한글')
    expect(container.textContent).not.toContain('Shortened for the model')
    expect(container.textContent).not.toContain('diff --git')
    expect(container.querySelector('[data-file-diff]')).toBeNull()

    const row = complete(result)
    const meta = providerToolMeta(AgentProvider.GITHUB_COPILOT, row, {
      spanType: COPILOT_TOOL.View,
      request: parsed(start(args, COPILOT_TOOL.View)),
    })
    expect(meta?.copyableContent()).toBe('second\nthird\n한글')
  })

  it.each([
    '--- a/another.txt\n+++ b/another.txt\n@@ -2,1 +2,1 @@\n wrong file\n',
    '--- a/project/sample.txt\n+++ b/project/sample.txt\n@@ -2,1 +2,1 @@\n-before\n+after\n',
    '--- a/project/sample.txt\n+++ b/project/sample.txt\n@@ -2,9 +2,9 @@\n incomplete\n',
  ])('keeps the native read content when the detailed data is invalid or belongs to another file', (detailedContent) => {
    const { container } = renderResult({ path: '/project/sample.txt' }, COPILOT_TOOL.View, {
      result: { content: 'Actual file content', detailedContent },
    })
    expect(container.textContent).toContain('Actual file content')
  })

  it('displays the full native result when the model receives a shorter one', () => {
    const { container } = renderResult({ command: 'printf complete-output' }, COPILOT_TOOL.Bash, {
      result: { content: 'Short model summary', detailedContent: 'Complete output\nKeep every line.' },
    })
    expect(container.textContent).toContain('Complete output')
    expect(container.textContent).toContain('Keep every line.')
    expect(container.textContent).not.toContain('Short model summary')
  })

  it('renders the native shell completion metadata as a command result', () => {
    const { container } = renderResult({ command: 'printf command-output' }, COPILOT_TOOL.Bash, {
      result: {
        content: 'command-output\n<shellId: 0 completed with exit code 0>',
        contents: [{ type: 'shell_exit', shellId: '0', exitCode: 0, cwd: '/project', outputPreview: 'command-output\n' }],
      },
    })
    expect(container.textContent).toContain('command-output')
    expect(container.textContent).not.toContain('shell_exit')
    expect(container.textContent).not.toContain('<shellId:')
    expect(container.textContent).toContain('Shell ID:0')
    expect(container.textContent).toContain('Directory:/project')
  })

  it('extracts the shell status trailer without showing it as output', () => {
    const { container } = renderResult({ command: 'python3 sample.py' }, COPILOT_TOOL.Bash, {
      success: false,
      result: { content: 'invalid argument\n<shellId: 0 completed with exit code 2>' },
    })
    expect(container.textContent).toContain('invalid argument')
    expect(container.textContent).toContain('Error (exit 2)')
    expect(container.textContent).not.toContain('<shellId:')
  })

  it('retains native images and structured values beside the command output', () => {
    const args = { command: 'render-artifact' }
    const result = {
      result: {
        content: 'Rendered an image.',
        structuredContent: { count: 0, enabled: false, text: '' },
        contents: [
          { type: 'shell_exit', shellId: '0', exitCode: 0 },
          { type: 'image', mimeType: 'image/png', data: 'aW1hZ2U=' },
        ],
      },
    }
    const row = complete(result)
    const options = { spanType: COPILOT_TOOL.Bash, request: parsed(start(args, COPILOT_TOOL.Bash)) }
    const meta = providerToolMeta(AgentProvider.GITHUB_COPILOT, row, options)
    expect(meta?.copyableContent()).toContain('Rendered an image.')
    expect(meta?.copyableContent()).toContain('false')
    expect(meta?.copyableContent()).toContain('0')
    expect(providerRowImages(AgentProvider.GITHUB_COPILOT, row, options)).toEqual([{ mimeType: 'image/png', data: 'aW1hZ2U=' }])
    const { container } = renderResult(args, COPILOT_TOOL.Bash, result)
    expect(container.textContent).toContain('Rendered an image.')
    expect(container.textContent).toContain('"enabled": false')
    expect(container.querySelectorAll('img')).toHaveLength(1)
  })

  it('preserves a raw error message that carries no content block', () => {
    const { container } = renderResult({ path: '/missing.py' }, COPILOT_TOOL.View, {
      success: false,
      error: { message: 'File does not exist', code: 'ENOENT' },
    })
    expect(container.textContent).toContain('File does not exist')
    expect(container.textContent).toContain('Error')
  })

  // A failed call states its reason in `error`. Reading the partial `result` there
  // would show whatever output it managed and hide why it stopped.
  it('prefers the error reason over a partial result on a failure', () => {
    const { container } = renderResult({ command: 'run-check' }, COPILOT_TOOL.Bash, {
      success: false,
      result: { content: 'partial output' },
      error: { message: 'The check could not start.' },
    })
    expect(container.textContent).toContain('The check could not start.')
    expect(container.textContent).not.toContain('partial output')
  })

  it('shows one failure status for a native agent result', () => {
    const { container } = renderResult({ description: 'Inspect project' }, COPILOT_TOOL.Task, {
      success: false,
      error: { message: 'The child failed.' },
    })
    expect(container.textContent).toContain('Agent "Inspect project" failed')
    expect(container.querySelectorAll('svg.lucide-circle-alert')).toHaveLength(1)
  })

  it('shows the launch instruction in the request row and the report in the result row', () => {
    const args = { description: 'Inspect the project', prompt: 'Read the entry points.', agent_type: 'explore', mode: 'sync' }
    const request = renderRow(start(args, COPILOT_TOOL.Task))
    expect(request.container.textContent).toContain('Read the entry points.')
    expect(request.container.textContent).not.toContain('The entry points exist.')

    const { container } = renderResult(args, COPILOT_TOOL.Task, {
      result: { content: '**Findings**\n\nThe entry points exist.' },
    })
    expect(container.textContent).toContain('Agent "Inspect the project" completed')
    expect(container.querySelector('strong')?.textContent).toBe('Findings')
  })

  it('keeps a matching file whose name starts with the no-match phrase', () => {
    const { container } = renderResult({ pattern: 'needle', paths: ['No matches found.ts'] }, COPILOT_TOOL.Grep, {
      result: { content: 'No matches found.ts:needle' },
    })
    expect(container.textContent).toContain('No matches found.ts:needle')
  })

  it('counts matches when the requested context is zero', () => {
    const { container } = renderResult({ pattern: 'needle', paths: ['/project'], C: 0 }, COPILOT_TOOL.Grep, {
      result: { content: 'a.ts:needle\na.ts:needle again' },
    })
    expect(container.textContent).toContain('2 matches in 1 file')
  })

  // Copilot writes a match as `path:line:text`, or as `path:text` when the reader asked
  // for no line numbers. The file count has to read both, so it cuts at the line number
  // when there is one and at the first colon when there is not.
  it('counts the files of a numbered grep output', () => {
    const { container } = renderResult({ pattern: 'needle', paths: ['/project'] }, COPILOT_TOOL.Grep, {
      result: { content: 'a.ts:12:needle\na.ts:40:needle again\nb.ts:3:needle' },
    })
    expect(container.textContent).toContain('3 matches in 2 files')
  })

  // An absolute Windows path carries its own colon. Cutting at the FIRST one filed
  // every match under the drive letter, so two files read as one.
  it('counts a Windows path as one file, not as its drive letter', () => {
    const { container } = renderResult({ pattern: 'needle', paths: ['C:\\repo'] }, COPILOT_TOOL.Grep, {
      result: { content: 'C:\\repo\\a.ts:12:needle\nC:\\repo\\b.ts:3:needle' },
    })
    expect(container.textContent).toContain('2 matches in 2 files')
  })

  it('does not count context lines when the native flag carries a leading dash', () => {
    const { container } = renderResult({ 'pattern': 'needle', 'paths': ['/project'], '-C': 1 }, COPILOT_TOOL.Grep, {
      result: { content: 'a.ts:before\na.ts:needle\na.ts:after' },
    })
    expect(container.textContent).not.toContain('Found 3 matches')
    expect(container.textContent).toContain('a.ts:needle')
  })

  it('shows a single native path-array target for a glob request', () => {
    const { container } = renderRow(start({ pattern: '*.ts', paths: ['/project'] }, COPILOT_TOOL.Glob))
    expect(container.textContent).toContain('/project')
  })

  it('shows every requested search path from the native array', () => {
    const { container } = renderRow(start({ pattern: 'answer', paths: ['/project/one', '/project/two'], output_mode: 'content' }, COPILOT_TOOL.Grep))
    expect(container.textContent).toContain('/project/one')
    expect(container.textContent).toContain('/project/two')
  })

  it('renders a native file search as a search rather than a file read', () => {
    const args = { pattern: '*.py', paths: '/project' }
    // The pair states the pattern once, on the request row that opens it.
    const request = renderRow(start(args, COPILOT_TOOL.Glob))
    expect(request.container.textContent).toContain('*.py')
    expect(request.container.textContent).toContain('/project')

    const { container } = renderResult(args, COPILOT_TOOL.Glob, {
      result: { content: '/project/first.py\n/project/second.py' },
    })
    expect(container.textContent).toContain('Found 2 files')
    expect(container.textContent).toContain('second.py')
  })

  it('renders native grep output without inventing missing line numbers', () => {
    const { container } = renderResult({ pattern: 'answer', paths: '/project', output_mode: 'content' }, COPILOT_TOOL.Grep, {
      result: { content: '/project/a.py:answer = 42\n/project/a.py:print(answer)' },
    })
    expect(container.textContent).toContain('2 matches in 1 file')
    expect(container.textContent).toContain('/project/a.py:answer = 42')
    expect(container.textContent).not.toContain('/project/a.py:1:')
  })

  it('renders the file content the view tool returns with its requested range', () => {
    const { container } = renderResult({ path: '/project/sample.py', view_range: [5, 6] }, COPILOT_TOOL.View, {
      result: { content: 'answer = 42\nprint(answer)' },
    })
    expect(container.textContent).toContain('answer = 42')
    expect(container.textContent).toContain('5')
    expect(container.textContent).toContain('6')
  })

  it('renders a pending file creation from the text patch request', () => {
    const { container } = renderRow(start({ input: '*** Begin Patch\n*** Add File: created.txt\n+First line\n+Second line\n*** End Patch\n' }, COPILOT_TOOL.ApplyPatch))
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('created.txt (2 lines)')
    expect(container.textContent).toContain('Requested changes')
    expect(container.querySelector('[data-file-diff]')?.textContent).toContain('First line')
  })

  it('shows every requested file in a patch with several operations', () => {
    const { container } = renderRow(start({ input: '*** Begin Patch\n*** Update File: sample.ts\n@@\n-before\n+after\n*** Delete File: obsolete.ts\n*** End Patch' }, COPILOT_TOOL.ApplyPatch))
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('2 files changed')
    expect(container.textContent?.match(/sample\.ts/g)).toHaveLength(1)
    expect(container.textContent?.match(/obsolete\.ts/g)).toHaveLength(1)
    expect(container.textContent).toContain('+1')
    expect(container.textContent).toContain('-1')
    expect(container.textContent).toContain('Requested changes')
    expect(container.querySelector('[data-file-diff]')?.textContent).toContain('after')
  })

  it('shows both paths for a requested move', () => {
    const { container } = renderRow(start({ input: '*** Begin Patch\n*** Update File: old name.ts\n*** Move to: new name.ts\n*** End Patch' }, COPILOT_TOOL.ApplyPatch))
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('old name.ts → new name.ts')
    expect(container.querySelector('[data-file-diff]')).toBeNull()
  })

  it('retains an incomplete patch as readable request text', () => {
    const patch = '*** Begin Patch\n*** Add File: incomplete.ts\n+unfinished'
    const { container } = renderRow(start({ input: patch }, COPILOT_TOOL.ApplyPatch))
    expect(container.querySelector('[data-file-diff]')).toBeNull()
    expect(container.textContent).toContain(COPILOT_TOOL.ApplyPatch)
  })

  it('renders the requested checklist as a to-do list', () => {
    const { container } = renderRow(start({ todos: '- [x] Read the code\n- [ ] Write the test' }, COPILOT_TOOL.UpdateTodo))
    expect(container.textContent).toContain('Read the code')
    expect(container.textContent).toContain('Write the test')
  })

  // The title of a file-change row is composed from its REQUEST, and a failure keeps
  // that request: `RequestedChangesBody` already refuses the diff, so the list draws
  // nothing extra and only the header words change.
  //
  // The assertion reads the TITLE the kind composes rather than the paired row's
  // header. A result row that has its request row beside it draws no header of its own
  // -- `ToolMessageLayout` states that rule -- so the header of THIS row is the outcome
  // word alone, and the title reaches a reader on every row that stands by itself.
  it('titles a failed edit with the file it asked to change, and draws no diff', () => {
    const args = { command: 'str_replace', path: '/project/parity.ts', old_str: 'before', new_str: 'after' }
    const row = complete({ success: false, error: { message: 'no match for the old text' } })
    const call = providerToolCall(AgentProvider.GITHUB_COPILOT, row, { spanType: COPILOT_TOOL.StrReplaceEditor, request: parsed(start(args, COPILOT_TOOL.StrReplaceEditor)) })!
    expect(titleTextOf(call)).toContain('parity.ts')

    const { container } = renderResult(args, COPILOT_TOOL.StrReplaceEditor, { success: false, error: { message: 'no match for the old text' } })
    expect(container.textContent).toContain('no match for the old text')
    // The failure landed nothing, so neither half of the diff draws.
    expect(container.textContent).not.toContain('Requested changes')
    expect(container.querySelector('[data-file-diff]')).toBeNull()
    expect(container.textContent).not.toContain('before')
  })

  it('titles a failed move with both of the paths it asked for', () => {
    const args = { source: '/project/old name.ts', path: '/project/new name.ts' }
    const row = complete({ success: false, error: { message: 'the destination exists' } })
    const call = providerToolCall(AgentProvider.GITHUB_COPILOT, row, { spanType: COPILOT_TOOL.Move, request: parsed(start(args, COPILOT_TOOL.Move)) })!
    expect(titleTextOf(call)).toBe('/project/old name.ts \u2192 /project/new name.ts')

    const { container } = renderResult(args, COPILOT_TOOL.Move, { success: false, error: { message: 'the destination exists' } })
    expect(container.textContent).toContain('the destination exists')
  })

  // `ProseResultBody` picks the markdown body for `markdown` and a `<pre>` block for
  // `plain`, so a roster answered as plain draws its own asterisks and table pipes.
  it('draws a subagent roster as markdown rather than as raw characters', () => {
    const { container } = renderResult({}, COPILOT_TOOL.ListAgents, { result: { content: '- **explore** reads the code' } })
    expect(container.querySelector('li strong')?.textContent).toBe('explore')
    expect(container.textContent).not.toContain('**explore**')
  })

  it('draws a completion report as markdown rather than as raw characters', () => {
    const { container } = renderResult({}, COPILOT_TOOL.TaskComplete, { result: { content: '## Result\n\n- Read **two** files.' } })
    expect(container.querySelector('h2')?.textContent).toBe('Result')
    expect(container.querySelector('li strong')?.textContent).toBe('two')
    expect(container.textContent).not.toContain('## Result')
  })

  // The three kinds whose answer is one composed line keep the `<pre>` block, which
  // states the runtime's own line breaks exactly as it sent them.
  it('draws a scratch-board note as plain text', () => {
    const { container } = renderResult({}, COPILOT_TOOL.ContextBoard, { result: { content: 'note: keep **these** bytes' } })
    expect(container.querySelector('strong')).toBeNull()
    expect(container.textContent).toContain('note: keep **these** bytes')
  })

  // Copilot carries every picture in `extraContent`, so a failure path that drops it
  // takes the image with it -- and the image tab's index list shrinks by the same one.
  it.each([
    [COPILOT_TOOL.View, { path: '/p/a.png' }],
    [COPILOT_TOOL.Grep, { pattern: 'needle' }],
    [COPILOT_TOOL.WebFetch, { url: 'https://example.com' }],
  ])('draws the picture a failed %s attached', (toolName, args) => {
    const failure = { success: false, error: { message: 'it broke', contents: [{ type: 'image', mimeType: 'image/png', data: 'aW1hZ2U=' }] } }
    const row = complete(failure)
    const options = { spanType: toolName, request: parsed(start(args, toolName)) }
    expect(providerRowImages(AgentProvider.GITHUB_COPILOT, row, options)).toMatchObject([{ mimeType: 'image/png', data: 'aW1hZ2U=' }])
    const { container } = renderResult(args, toolName, failure)
    expect(container.querySelectorAll('img')).toHaveLength(1)
  })

  // A running edit has only ASKED to change the file; the finished one changed it.
  it('separates a requested file change from a confirmed one', () => {
    const args = { command: 'str_replace', path: '/project/parity.ts', old_str: 'before', new_str: 'after' }
    const request = renderRow(start(args, COPILOT_TOOL.StrReplaceEditor))
    expect(request.container.textContent).toContain('Requested changes')
    expect(request.container.querySelector('[data-file-diff]')?.textContent).toContain('after')

    const { container } = renderResult(args, COPILOT_TOOL.StrReplaceEditor, { result: { content: 'Saved' } })
    expect(container.textContent).not.toContain('Requested changes')
    expect(container.querySelector('[data-file-diff]')?.textContent).toContain('after')
  })

  it('renders a native file creation as a written file', () => {
    const args = { path: '/project/created.ts', file_text: 'export const created = true\n' }
    const { container } = renderResult(args, COPILOT_TOOL.Create, { result: { content: 'Created' } })
    expect(container.querySelector('[data-file-diff]')?.textContent).toContain('export const created = true')
  })

  // A command with no description states the command, as every other provider does.
  it('does not put the tool name above the command it ran', () => {
    const described = renderRow(start({ command: 'bun test', description: 'Run the tests' }, COPILOT_TOOL.Bash))
    expect(described.container.querySelector(`.${toolUseHeader}`)?.textContent).toContain('Run the tests')

    const { container } = renderRow(start({ command: 'bun test' }, COPILOT_TOOL.Bash))
    const header = container.querySelector(`.${toolUseHeader}`)?.textContent
    expect(header).toContain('Run command')
    expect(header).not.toContain(COPILOT_TOOL.Bash)
  })

  it('states how many tasks a to-do row holds', () => {
    const { container } = renderRow(start({ todos: '- [x] Read the code\n- [ ] Write the test' }, COPILOT_TOOL.UpdateTodo))
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toContain('2 tasks')
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).not.toContain(COPILOT_TOOL.UpdateTodo)
  })

  // A recognized tool keeps its own body: a read stays a read, and the rich blocks
  // ride beside it instead of replacing the file view.
  it('keeps a recognized tool body when the result also carries rich content', () => {
    const { container } = renderResult({ path: '/project/sample.py' }, COPILOT_TOOL.View, {
      result: {
        content: 'answer = 42',
        contents: [{ type: 'text', text: '**Read note**' }],
        structuredContent: { bytes: 0 },
      },
    })
    expect(container.textContent).toContain('answer = 42')
    expect(container.textContent).toContain('Read note')
    // The structured payload renders as part of the extra content now, without
    // the section label the legacy MCP body drew.
    expect(container.textContent).toContain('"bytes": 0')
  })

  // A tool this build does not recognize has no body of its own, so the rich
  // content IS its result.
  it('renders an unrecognized tool through the shared rich body', () => {
    const { container } = renderResult({ query: 'rendering' }, 'probe_echo', {
      result: { content: 'Result', contents: [{ type: 'text', text: '**Rich body**' }] },
    })
    expect(container.querySelector('strong')?.textContent).toBe('Rich body')
    expect(container.textContent).toContain('Arguments')
  })

  it('resolves the tool name from the span when the start row is absent', () => {
    const { container } = renderRow(complete({ result: { content: 'recovered output' } }), undefined, COPILOT_TOOL.Bash)
    expect(container.textContent).toContain('recovered output')
  })

  // No start row AND no span type: the completion has only its content to show, and
  // it must show that rather than a tool name it never read.
  it('shows an unmatched completion by its content alone', () => {
    const { container } = renderRow(complete({ result: { content: 'recovered output' } }), undefined, undefined)
    expect(container.textContent).toContain('recovered output')
    for (const invented of ['Run command', 'Read file', 'Search'])
      expect(container.textContent).not.toContain(invented)
  })
})

describe('a copilot tool row the turn interrupted', () => {
  // The runtime sends no completion for a call its turn cut short, so the worker
  // stores the START frame again. That copy is the call's RESULT, so the row reads
  // as one: no second request card under the Interrupted header, and no result body,
  // because the runtime reported none.
  const args = { command: 'bun test --coverage' }
  const request = start(args, COPILOT_TOOL.Bash)

  function renderRetained() {
    // The row's OWN parsed content carries the completion, which is what marks this
    // copy of the start frame as the call's end.
    const retained = { ...parsed(request), completion: MessageCompletion.INTERRUPTED }
    const sources = testMessageSources({ current: () => retained, request: () => parsed(request), role: () => 'result', visibleRows: () => ({ request: true, result: true }) })
    return render(() => renderMessageContent(
      request,
      { premeasureMode: true, spanType: COPILOT_TOOL.Bash, sources },
      { kind: 'tool_result' },
      AgentProvider.GITHUB_COPILOT,
      MessageCompletion.INTERRUPTED,
    ))
  }

  it('repeats neither the command nor its request card', () => {
    const { container } = renderRetained()
    expect(container.textContent).not.toContain(args.command)
    // The interruption is the row's only header. The request card above it already
    // states the tool and the command.
    expect([...container.querySelectorAll(`.${toolUseHeader}`)].map(node => node.textContent))
      .toEqual(['Interrupted'])
  })

  // The runtime reported no output, and every other provider's interrupted command
  // says exactly this, so the four transports read the same.
  it('states that the call produced no output', () => {
    const { container } = renderRetained()
    expect(container.textContent).toBe('Interrupted[no output]')
  })
})
