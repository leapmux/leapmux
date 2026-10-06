import type { MockModelRequestRecord, MockModelScenarioStatus } from './mockModelScript'
import { describe, expect, it, vi } from 'vitest'
import { waitForNewestModelRequest } from './newestModelRequest'

function record(text: string): MockModelRequestRecord {
  return { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { text } }
}

function script(...snapshots: MockModelRequestRecord[][]) {
  const status = vi.fn<() => Promise<MockModelScenarioStatus>>()
  for (const requests of snapshots)
    status.mockResolvedValueOnce({ requests } as unknown as MockModelScenarioStatus)
  status.mockResolvedValue({ requests: snapshots.at(-1) ?? [] } as unknown as MockModelScenarioStatus)
  return { status }
}

function textOf(request: MockModelRequestRecord) {
  const text = (request.body as { text: string }).text
  return text.startsWith('match') ? text : null
}

describe('waitForNewestModelRequest', () => {
  it('returns the value of the newest request that carries one', async () => {
    expect(await waitForNewestModelRequest(script([record('match-old'), record('other'), record('match-new'), record('later')]), textOf)).toBe('match-new')
  })

  it('reads the requests again until one carries a value', async () => {
    const modelScript = script([record('other')], [record('other'), record('match-late')])
    expect(await waitForNewestModelRequest(modelScript, textOf)).toBe('match-late')
    expect(modelScript.status).toHaveBeenCalledTimes(2)
  })

  it('keeps a falsy value that is not null', async () => {
    expect(await waitForNewestModelRequest(script([record('any')]), () => '')).toBe('')
  })

  it('ends at once with the error of a read that refuses a request', async () => {
    const modelScript = script([record('broken')])
    const failure = new Error('The reader refused the request.')
    await expect(waitForNewestModelRequest(modelScript, () => {
      throw failure
    })).rejects.toThrow(failure.message)
    expect(modelScript.status).toHaveBeenCalledOnce()
  })
})
