import { describe, expect, it } from 'vitest'
import { compactedLabel } from '../../../src/components/chat/notificationEntries'
import { COMPACTION_NOTICE_TEXT, compactionChatSelector } from './compaction'
import { CHAT_SCROLL_CONTAINER } from './ui'

describe('COMPACTION_NOTICE_TEXT', () => {
  // The label is app-owned. If the app rewords the notice and this constant
  // does not follow, the e2e looks for a row that never renders.
  it('is the label the app draws for a compaction', () => {
    expect(compactedLabel(undefined)).toBe(COMPACTION_NOTICE_TEXT)
  })

  it('stays the prefix of a notice that carries a token detail', () => {
    const detailed = compactedLabel({ trigger: 'manual', pre: 12040, post: 3000 })
    expect(detailed.startsWith(COMPACTION_NOTICE_TEXT)).toBe(true)
  })
})

describe('compactionChatSelector', () => {
  it('scopes the chat container to the visible transcript', () => {
    expect(compactionChatSelector()).toBe(`${CHAT_SCROLL_CONTAINER}:visible`)
    expect(compactionChatSelector()).toContain(':visible')
  })
})
