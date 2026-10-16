import type { MockModelRule } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { matchesRequest, parseScenarioSpec, resolveStepCaptures } from '../helpers/mockModelScript'
import { codexAsyncExecRules } from './asyncExec'

function request(input: unknown[]) {
  return { protocol: 'openai-responses' as const, systemText: '', userText: '', body: { input } }
}

function output(text: string) {
  return { type: 'custom_tool_call_output', call_id: 'actual-call', output: text }
}

function rule(name: string, steering?: string): MockModelRule {
  const rules = parseScenarioSpec({ steps: [], rules: codexAsyncExecRules(steering) }).rules
  const result = rules?.find(item => item.name === name)
  if (!result)
    throw new Error('The native continuation rule is absent.')
  return result
}

describe('codexAsyncExecRules', () => {
  it('uses the newest native cell ID after a yield', () => {
    const pending = rule('codex-await-code-cell')
    const actual = request([output('Script running with cell ID 2\nOutput:\n'), output('Script running with cell ID 71\nOutput:\n')])
    expect(matchesRequest(pending.when, actual)).toBe(true)
    const resolved = resolveStepCaptures(pending.respond, actual.body)
    expect(resolved).toMatchObject({ step: { toolCalls: [{ name: 'wait', arguments: { cell_id: '71' } }] } })
  })

  it('does not repeat a wait after completion even when history contains an earlier yield', () => {
    const pending = rule('codex-await-code-cell')
    expect(matchesRequest(pending.when, request([output('Script running with cell ID 2\n'), output('Script completed\nOutput:\n42')]))).toBe(false)
  })

  it('matches a native command session and preserves zero', () => {
    const pending = rule('codex-await-command-session')
    const actual = request([output('Script completed\nOutput:\n{"session_id":0,"output":"partial"}')])
    expect(matchesRequest(pending.when, actual)).toBe(true)
    const resolved = resolveStepCaptures(pending.respond, actual.body)
    if (!('step' in resolved))
      throw new Error('The actual native session ID did not match.')
    expect(resolved.step.toolCalls?.[0]?.input).toContain('Number("0")')
    expect(resolved.step.toolCalls?.[0]?.input).toContain('tools.write_stdin')
    expect(matchesRequest(pending.when, request([output('{"exit_code":0,"output":"done"}')]))).toBe(false)
  })

  it('handles an exact steering message after the native yield', () => {
    const text = 'Append [steered].'
    const pending = rule('codex-await-steered-cell', text)
    const earlier = output('Script running with cell ID 19\nOutput:\n')
    const message = (value: string) => ({ type: 'message', role: 'user', content: [{ type: 'input_text', text: value }] })
    expect(matchesRequest(pending.when, request([earlier, message(text)]))).toBe(true)
    expect(matchesRequest(pending.when, request([earlier, message('Append s. extra')]))).toBe(false)
    expect(matchesRequest(pending.when, request([message(text)]))).toBe(false)
    expect(pending.once).toBe(true)
  })

  it('rejects empty steering text', () => {
    expect(() => codexAsyncExecRules(' ')).toThrow('nonempty text')
  })
})
