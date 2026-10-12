import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { deferred } from '../../../src/test-support/async'
import { cleanupOnFailure, withCleanup } from '../helpers/cleanup'
import { MOCK_MODEL_IDS, MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { createMockModelServer } from '../helpers/mockModelServer'
import { startModelScript } from '../helpers/modelScriptFixture'
import { deepseekHarnessCompletedReport, finishDeepseekHarnessChild } from './childReportCompletion'
import { deepseekHarnessChildReportRule } from './childReports'

describe('finishDeepseekHarnessChild', () => {
  it('preserves a child finish failure before report work starts', async () => {
    const failure = new Error('The native child finish failed.')
    let reportCalled = false
    await expect(finishDeepseekHarnessChild(async () => {
      throw failure
    }, async () => {
      reportCalled = true
    })).rejects.toBe(failure)
    expect(reportCalled).toBe(false)
  })

  it('preserves an exact parent report failure', async () => {
    const failure = new Error('The native parent report failed.')
    await expect(finishDeepseekHarnessChild(async () => {}, async () => {
      throw failure
    })).rejects.toBe(failure)
  })

  it.each(['finished and will do no further work unless you send it more.', 'was stopped before it finished.'])('waits for the actual held parent report: %s', async (ending) => {
    const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
    const lifecycle = await cleanupOnFailure(() => startModelScript(server.url), () => server.close())
    await withCleanup(async () => {
      const script = lifecycle.script
      const gate = 'native-parent-report-response'
      const rule = deepseekHarnessChildReportRule('native-child-session')
      await script.rule({ ...rule, respond: { ...rule.respond, gate } })
      const reply = fetch(`${server.url}/v1/messages`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', 'x-api-key': MODEL_KEY },
        body: JSON.stringify({
          model: 'deepseek-flash',
          stream: false,
          max_tokens: 32,
          messages: [
            { role: 'user', content: [{ type: 'text', text: script.prompt('The initial parent task created its native child.') }] },
            { role: 'assistant', content: [{ type: 'text', text: 'The initial native parent turn completed.' }] },
            { role: 'user', content: [{ type: 'text', text: `Background subagent native-child-session ${ending}\nThe child result.` }] },
          ],
        }),
      }).then(async (response) => {
        expect(response.status).toBe(200)
        await response.arrayBuffer()
      })
      const reportEntered = deferred<'report-entered'>()
      const finished = finishDeepseekHarnessChild(
        async () => { await script.waitForGate(gate) },
        async () => {
          reportEntered.resolve('report-entered')
          await reply
        },
      ).then(() => 'finish-returned' as const)
      await withCleanup(async () => {
        await expect(Promise.race([reportEntered.promise, finished])).resolves.toBe('report-entered')
        expect((await script.status()).pendingGates).toContain(gate)
        await script.releaseGateIfHeld(gate)
        await expect(finished).resolves.toBe('finish-returned')
        const status = await script.status()
        expect(status.ruleMatches[rule.name]).toBe(1)
        expect(status.pendingGates).not.toContain(gate)
        expect(status.requests.find(request => request.rule === rule.name)?.response?.status).toBe(200)
      }, async () => {
        await script.releaseGateIfHeld(gate)
        await reply
        await finished
      })
    }, () => withCleanup(() => lifecycle.finish(false), () => server.close()))
  })
})

describe('deepseekHarnessCompletedReport', () => {
  function request(): MockModelRequestRecord {
    return { protocol: 'anthropic-messages', path: '/v1/messages', response: { status: 200, headers: {} }, body: { dsh_session_log: { session: { id: 'native-parent' }, events: [{ type: 'turn/start', data: { turn: 2 } }] } } }
  }
  function ending(turn: number, kind = 'completed') {
    return { type: 'turn/end', data: { turn, reason: { kind } } }
  }

  it('waits for the exact report response and parent turn instead of an earlier completion', () => {
    const record = request()
    const pending = { ...record }
    delete pending.response
    expect(deepseekHarnessCompletedReport(pending, 'native-parent', [ending(2)])).toBe(false)
    expect(deepseekHarnessCompletedReport(record, 'native-parent', [])).toBe(false)
    expect(deepseekHarnessCompletedReport(record, 'native-parent', [ending(1)])).toBe(false)
    expect(deepseekHarnessCompletedReport(record, 'native-parent', [ending(1), ending(2)])).toBe(true)
  })

  it('rejects another Session and model protocol', () => {
    expect(() => deepseekHarnessCompletedReport(request(), 'another-parent', [ending(2)])).toThrow('stored parent Session')
    expect(() => deepseekHarnessCompletedReport({ ...request(), protocol: 'openai-responses' }, 'native-parent', [ending(2)])).toThrow('Session and model protocol')
  })

  it.each([0, -1, 0.5, undefined, Number.MAX_SAFE_INTEGER + 1])('rejects an invalid native turn identity: %j', (turn) => {
    const record = { ...request(), body: { dsh_session_log: { session: { id: 'native-parent' }, events: [{ type: 'turn/start', data: { turn } }] } } }
    expect(() => deepseekHarnessCompletedReport(record, 'native-parent', [ending(2)])).toThrow('native turn identity')
  })

  it.each(['error', 'aborted', 'interrupted', 'max-tokens'])('rejects a parent report that ends with %s', (kind) => {
    expect(() => deepseekHarnessCompletedReport(request(), 'native-parent', [ending(2, kind)])).toThrow(`did not complete: ${kind}`)
  })

  it('rejects a failed response and duplicate stored completions', () => {
    expect(() => deepseekHarnessCompletedReport({ ...request(), response: { status: 500, headers: {} } }, 'native-parent', [ending(2)])).toThrow('status 500')
    expect(() => deepseekHarnessCompletedReport(request(), 'native-parent', [ending(2), ending(2)])).toThrow('duplicate stored turn completions')
  })
})
