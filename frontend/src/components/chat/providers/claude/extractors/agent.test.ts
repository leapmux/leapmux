import { describe, expect, it } from 'vitest'
import { claudeAgentFromToolResult, claudeAgentResultBody, claudeAgentResultIsLaunch } from './agent'

const HARNESS_TEXT = 'Async agent launched successfully. (internal metadata — never quote it.)'

describe('claudeAgentFromToolResult', () => {
  it('returns null for a payload that is not an object', () => {
    expect(claudeAgentFromToolResult(undefined, 'text')).toBeNull()
  })

  it('defaults an absent status to completed', () => {
    expect(claudeAgentFromToolResult({ agentId: 'a1' }, '')?.status).toBe('completed')
  })

  // The block text is the report for a finished run, and the CLI's instructions
  // to the model for a launch -- so it backs the body in one case only.
  it('adopts the block text as the report for a finished run', () => {
    const source = claudeAgentFromToolResult({ status: 'completed', agentId: 'a1' }, 'A plain report.')
    expect(source?.content).toBe('A plain report.')
  })

  it('leaves a launch body empty rather than adopting the harness text', () => {
    const source = claudeAgentFromToolResult({ status: 'async_launched', agentId: 'a1' }, HARNESS_TEXT)
    expect(source?.content).toBe('')
  })

  // A synchronous result omits `description`. The paired tool_use input carries
  // the model-written task title, so the extractor fills the gap from it.
  it('takes the description from the paired tool_use input for a sync result', () => {
    const source = claudeAgentFromToolResult(
      { status: 'completed', agentId: 'a1' },
      'The report.',
      { description: 'SCAN triage + angle recommendation' },
    )
    expect(source?.description).toBe('SCAN triage + angle recommendation')
  })

  it('prefers a non-blank result description over the paired input', () => {
    const source = claudeAgentFromToolResult(
      { status: 'async_launched', description: 'From result', agentId: 'a1' },
      '',
      { description: 'From input' },
    )
    expect(source?.description).toBe('From result')
  })

  it('falls back to the paired input when the result description is blank', () => {
    const source = claudeAgentFromToolResult(
      { status: 'completed', description: '  ', agentId: 'a1' },
      '',
      { description: 'From input' },
    )
    expect(source?.description).toBe('From input')
  })

  it('keeps the agent ID fallback when neither payload supplies a description', () => {
    const source = claudeAgentFromToolResult(
      { status: 'completed', agentId: 'a1' },
      '',
      { description: '' },
    )
    expect(source?.description).toBe('')
    expect(source?.agentId).toBe('a1')
  })

  // The input side trims too: a whitespace-only input description is not a
  // real one, and the header would render `Agent "   " completed` without it.
  it('treats a whitespace-only paired input description as absent', () => {
    const source = claudeAgentFromToolResult(
      { status: 'completed', agentId: 'a1' },
      '',
      { description: '   ' },
    )
    expect(source?.description).toBe('')
  })
})

describe('agent report text', () => {
  it('joins the text blocks of a finished run', () => {
    const source = claudeAgentFromToolResult({
      status: 'completed',
      content: [{ type: 'text', text: 'First.' }, { type: 'text', text: 'Second.' }],
    }, '')
    expect(source?.content).toBe('First.\n\nSecond.')
  })

  // A subagent that takes a screenshot returns image blocks, and the card
  // renders markdown. Filtering to text alone dropped the picture entirely.
  it('keeps an image block as markdown', () => {
    const source = claudeAgentFromToolResult({
      status: 'completed',
      content: [
        { type: 'text', text: 'Here it is.' },
        { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AAA' } },
      ],
    }, '')
    expect(source?.content).toContain('Here it is.')
    expect(source?.content).toContain('![image](data:image/png;base64,AAA)')
  })

  it('keeps an external image as a link rather than an inline embed', () => {
    const source = claudeAgentFromToolResult({
      status: 'completed',
      content: [{ type: 'image', source: { type: 'url', url: 'https://example.test/a.png' } }],
    }, '')
    expect(source?.content).toBe('[image](https://example.test/a.png)')
  })

  // A non-array `content` yields no report, so a finished run falls back to the
  // block text exactly as it does when the field is absent.
  it('ignores a content field that is not an array', () => {
    const source = claudeAgentFromToolResult({ status: 'completed', content: 'oops' }, 'The block text.')
    expect(source?.content).toBe('The block text.')
  })
})

describe('claudeAgentResultIsLaunch', () => {
  it.each(['async_launched', 'remote_launched'])('reports %s as a launch', (status) => {
    expect(claudeAgentResultIsLaunch(claudeAgentFromToolResult({ status }, '')!)).toBe(true)
  })

  it('reports a finished run as not a launch', () => {
    const source = claudeAgentFromToolResult({ status: 'completed', content: [{ type: 'text', text: 'done' }] }, '')
    expect(claudeAgentResultIsLaunch(source!)).toBe(false)
  })

  // A finished synchronous run carries no output file, so "no report but an
  // output file" is a launch whatever the CLI calls the status. Without this a
  // launch status added later falls through to the finished path, where the
  // body becomes the harness instructions.
  it('reports an unknown status with an output file as a launch', () => {
    const source = claudeAgentFromToolResult({ status: 'queued', outputFile: '/tmp/a.output' }, HARNESS_TEXT)
    expect(claudeAgentResultIsLaunch(source!)).toBe(true)
    expect(source?.content).toBe('')
  })

  it('does not call a run with a report a launch merely because it gives an output file', () => {
    const source = claudeAgentFromToolResult({
      status: 'completed',
      outputFile: '/tmp/a.output',
      content: [{ type: 'text', text: 'The report.' }],
    }, '')
    expect(claudeAgentResultIsLaunch(source!)).toBe(false)
  })
})

describe('claudeAgentResultBody', () => {
  it('prefers the report over the prompt', () => {
    const source = claudeAgentFromToolResult({
      status: 'completed',
      prompt: 'Do the thing.',
      content: [{ type: 'text', text: 'Did it.' }],
    }, '')
    expect(claudeAgentResultBody(source!)).toBe('Did it.')
  })

  it('falls back to the prompt for a launch, which has no report yet', () => {
    const source = claudeAgentFromToolResult({ status: 'async_launched', prompt: 'Do the thing.' }, HARNESS_TEXT)
    expect(claudeAgentResultBody(source!)).toBe('Do the thing.')
  })

  it('is empty when the payload carries neither', () => {
    const source = claudeAgentFromToolResult({ status: 'async_launched', agentId: 'a1' }, '')
    expect(claudeAgentResultBody(source!)).toBe('')
  })
})
