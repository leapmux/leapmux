import { describe, expect, it } from 'vitest'
import { TRUNCATION_NOTICE } from './truncationNotice'

describe('the shared truncation notice (TRUNCATION_NOTICE)', () => {
  it('is one sentence, so a reader learns it once', () => {
    expect(TRUNCATION_NOTICE).toBe('Output truncated')
  })
})
