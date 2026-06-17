import { describe, expect, it } from 'vitest'
import { codexCommandFromItem, codexUnwrapCommand, stripToolUseHeaderFromOutput } from './commandExecution'

describe('codexUnwrapCommand', () => {
  it('strips /bin/zsh -lc shell wrapper', () => {
    expect(codexUnwrapCommand('/bin/zsh -lc \'echo hi\'')).toBe('echo hi')
  })

  it('passes through unwrapped commands', () => {
    expect(codexUnwrapCommand('echo hi')).toBe('echo hi')
  })
})

describe('codexCommandFromItem', () => {
  it('returns null for non-commandExecution items', () => {
    expect(codexCommandFromItem(null)).toBeNull()
    expect(codexCommandFromItem({ type: 'agentMessage' })).toBeNull()
  })

  it('extracts the structured payload', () => {
    expect(codexCommandFromItem({
      type: 'commandExecution',
      command: 'echo hi',
      aggregatedOutput: 'hi',
      exitCode: 0,
      durationMs: 10,
      status: 'completed',
    })).toEqual({
      output: 'hi',
      exitCode: 0,
      durationMs: 10,
      isError: false,
    })
  })

  it('marks isError when status=failed', () => {
    const source = codexCommandFromItem({
      type: 'commandExecution',
      aggregatedOutput: '',
      status: 'failed',
    })
    expect(source?.isError).toBe(true)
  })

  it('marks isError when exit code is non-zero', () => {
    const source = codexCommandFromItem({
      type: 'commandExecution',
      aggregatedOutput: '',
      exitCode: 5,
      status: 'completed',
    })
    expect(source?.isError).toBe(true)
    expect(source?.exitCode).toBe(5)
  })
})

describe('stripToolUseHeaderFromOutput', () => {
  it('returns the output unchanged when no header chrome is present', () => {
    const out = 'line 1\nline 2\nline 3'
    expect(stripToolUseHeaderFromOutput(out)).toBe(out)
  })

  it('strips a single-line header div without eating the real output below it', () => {
    // Regression: the old depth=1 seed skipped the header line's own tags, so a
    // self-balanced single-line <div...>...</div> never closed and the walk ran to
    // EOF, deleting every following line of genuine command output.
    const out = [
      'Building...',
      '<div class="toolStyles_toolUseHeader__abc">Run command</div>',
      'Build OK',
      'tests pass',
    ].join('\n')
    expect(stripToolUseHeaderFromOutput(out)).toBe('Building...\nBuild OK\ntests pass')
  })

  it('strips a single-line wrapper+header div, keeping surrounding output', () => {
    const out = [
      'before',
      '<div class="toolMessage__x"><div class="toolUseHeader__abc">Run</div></div>',
      'after',
    ].join('\n')
    expect(stripToolUseHeaderFromOutput(out)).toBe('before\nafter')
  })

  it('strips a multi-line header block whose <div opens on the previous line', () => {
    const out = [
      'prefix',
      '<div',
      '  class="toolStyles_toolUseHeader__abc"',
      '>',
      '  <span>Run command</span>',
      '</div>',
      'real output',
    ].join('\n')
    expect(stripToolUseHeaderFromOutput(out)).toBe('prefix\nreal output')
  })

  it('does not over-consume past a single-line header when later output contains a stray </div>', () => {
    const out = [
      '<div class="toolUseHeader__abc">Run</div>',
      'output line',
      'looks like </div> in text',
      'final line',
    ].join('\n')
    expect(stripToolUseHeaderFromOutput(out)).toBe('output line\nlooks like </div> in text\nfinal line')
  })

  it('does not pop a balanced <div></div> on the previous line nor eat output below the header', () => {
    // Regression: the prev-line pop fired on ANY line containing `<div`, so a
    // self-contained BALANCED div before the header was deleted AND its leftover
    // phantom depth swallowed a line of real output below the single-line header.
    const out = [
      'real output A',
      '<div class="some-other-block">unrelated balanced</div>',
      '<div class="toolStyles_toolUseHeader__abc">Run</div>',
      'real output B',
    ].join('\n')
    expect(stripToolUseHeaderFromOutput(out)).toBe(
      'real output A\n<div class="some-other-block">unrelated balanced</div>\nreal output B',
    )
  })
})
