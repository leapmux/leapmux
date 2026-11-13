import { describe, expect, it } from 'vitest'
import { digestFields } from './fieldDigest'
import { answerFieldDigestRequest } from './fieldDigestWorker'

describe('answerFieldDigestRequest', () => {
  it('answers under the request id with the digest of the fields', () => {
    const fields = [
      new TextEncoder().encode('agent-1'),
      Uint8Array.of(1, 2, 3),
    ]

    expect(answerFieldDigestRequest({ id: 7, fields })).toEqual({
      id: 7,
      digest: digestFields(fields),
    })
  })

  it('answers an empty field list', () => {
    expect(answerFieldDigestRequest({ id: 0, fields: [] })).toEqual({
      id: 0,
      digest: digestFields([]),
    })
  })
})
