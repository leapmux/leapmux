import { describe, expect, it } from 'vitest'
import { KIRO_E2E_API_KEY, MOCK_COPILOT_GITHUB_TOKEN, MOCK_IDENTITY_TOKEN, MOCK_SESSION_TOKEN, MODEL_KEY } from './mockAgentEnvironment'
import { mockCredentialReceipt } from './mockCredentials'

describe('mockCredentialReceipt', () => {
  it('validates the native Google key without retaining its value', () => {
    expect(mockCredentialReceipt({ 'x-goog-api-key': MODEL_KEY })).toEqual({ kind: 'api-key', accepted: true })
    expect(mockCredentialReceipt({ 'x-goog-api-key': 'private-google-value' })).toEqual({ kind: 'api-key', accepted: false })
    expect(mockCredentialReceipt({ 'x-goog-api-key': [MODEL_KEY] })).toEqual({ kind: 'api-key', accepted: false })
    expect(mockCredentialReceipt({ 'authorization': 'Bearer private-value', 'x-goog-api-key': MODEL_KEY })).toEqual({ kind: 'api-key', accepted: false })
  })

  it('validates each supplied URL key and refuses duplicate or conflicting keys', () => {
    expect(mockCredentialReceipt({}, [MODEL_KEY])).toEqual({ kind: 'api-key', accepted: true })
    expect(mockCredentialReceipt({}, ['private-url-key'])).toEqual({ kind: 'api-key', accepted: false })
    expect(mockCredentialReceipt({}, [MODEL_KEY, MODEL_KEY])).toEqual({ kind: 'api-key', accepted: false })
    expect(mockCredentialReceipt({ 'x-goog-api-key': MODEL_KEY }, ['private-url-key'])).toEqual({ kind: 'api-key', accepted: false })
  })
  it('accepts the exact fixture bearer that the actual Copilot model request uses', () => {
    const receipt = mockCredentialReceipt({ authorization: `Bearer ${MOCK_COPILOT_GITHUB_TOKEN}` })
    expect(receipt).toEqual({ kind: 'bearer', accepted: true })
    expect(Object.keys(receipt).sort()).toEqual(['accepted', 'kind'])
    expect(JSON.stringify(receipt)).not.toContain(MOCK_COPILOT_GITHUB_TOKEN)
  })

  it.each([
    { label: 'changed fixture', token: `${MOCK_COPILOT_GITHUB_TOKEN}changed` },
    { label: 'fixture prefix only', token: MOCK_COPILOT_GITHUB_TOKEN.slice(0, -1) },
    { label: 'another PAT-shaped value', token: 'github_pat_another_private_value' },
  ])('refuses the $label instead of accepting a credential pattern', ({ token }) => {
    const receipt = mockCredentialReceipt({ authorization: `Bearer ${token}` })
    expect(receipt).toEqual({ kind: 'bearer', accepted: false })
    expect(JSON.stringify(receipt)).not.toContain(token)
  })

  it('retains array and mixed-unknown refusal for the fixed Copilot fixture', () => {
    expect(Reflect.apply(mockCredentialReceipt, undefined, [{ authorization: [`Bearer ${MOCK_COPILOT_GITHUB_TOKEN}`] }])).toEqual({ kind: 'bearer', accepted: false })
    expect(mockCredentialReceipt({ 'authorization': `Bearer ${MOCK_COPILOT_GITHUB_TOKEN}`, 'x-api-key': 'other-private-key' })).toEqual({ kind: 'api-key', accepted: false })
    expect(mockCredentialReceipt({ 'api-key': [MOCK_COPILOT_GITHUB_TOKEN] })).toEqual({ kind: 'api-key', accepted: false })
  })

  for (const key of [MODEL_KEY, KIRO_E2E_API_KEY, MOCK_COPILOT_GITHUB_TOKEN, MOCK_IDENTITY_TOKEN, MOCK_SESSION_TOKEN]) {
    it(`accepts the known fake credential ${key}`, () => {
      expect(mockCredentialReceipt({ authorization: `Bearer ${key}` })).toEqual({ kind: 'bearer', accepted: true })
      expect(mockCredentialReceipt({ 'x-api-key': key })).toEqual({ kind: 'api-key', accepted: true })
    })
  }

  it('rejects absent, empty, malformed, and unknown credentials', () => {
    expect(mockCredentialReceipt({})).toEqual({ kind: 'none', accepted: false })
    for (const authorization of ['', 'Bearer ', 'Basic example', 'Bearer private-value', `Bearer ${MODEL_KEY} extra`])
      expect(mockCredentialReceipt({ authorization })).toEqual({ kind: 'bearer', accepted: false })
    expect(mockCredentialReceipt({ 'x-api-key': '' })).toEqual({ kind: 'api-key', accepted: false })
    expect(mockCredentialReceipt({ 'api-key': ['first', 'second'] })).toEqual({ kind: 'api-key', accepted: false })
  })

  it('checks every supplied credential and records no value', () => {
    const receipt = mockCredentialReceipt({ 'authorization': 'Bearer private-value', 'x-api-key': MODEL_KEY })
    expect(receipt).toEqual({ kind: 'api-key', accepted: false })
    expect(JSON.stringify(receipt)).not.toContain('private-value')
    expect(mockCredentialReceipt({ 'authorization': `Bearer ${MODEL_KEY}`, 'x-api-key': MODEL_KEY })).toEqual({ kind: 'api-key', accepted: true })
  })
})
