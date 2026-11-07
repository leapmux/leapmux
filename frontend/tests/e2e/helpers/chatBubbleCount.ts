import type { Page } from '@playwright/test'
import { withCleanup } from './cleanup'
import { chatScrollContainer, readAttached, scrollChatRowIntoView } from './ui'

export interface ChatBubbleWindow {
  top: number
  height: number
  contentHeight: number
  bubbles: Array<{ sequence: string, role: string, text: string }>
}

export interface ChatBubbleViewport {
  read: () => Promise<ChatBubbleWindow>
  reveal: () => Promise<void>
  scrollToEnd: () => Promise<void>
  scrollTo: (top: number) => Promise<void>
}

/** Count each matching Worker row once while walking overlapping virtual windows. */
export async function countMatchingChatBubbles(viewport: ChatBubbleViewport, role: string, text: string): Promise<number> {
  if (!text.trim())
    throw new Error('A chat bubble count requires nonempty text.')
  const readWindow = async () => {
    const window = await viewport.read()
    if (![window.top, window.height, window.contentHeight].every(Number.isFinite)
      || window.top < 0 || window.height <= 0 || window.contentHeight < 0) {
      throw new Error('The chat viewport requires finite scroll geometry and a positive height.')
    }
    return window
  }
  const initial = await readWindow()
  const atEnd = Math.max(0, initial.contentHeight - initial.height) - initial.top <= 1
  const sequences = new Set<string>()
  return withCleanup(async () => {
    await viewport.reveal()
    await viewport.scrollTo(0)
    const visited = new Set<string>()
    for (;;) {
      const window = await readWindow()
      const matching = new Set<string>()
      for (const bubble of window.bubbles) {
        if (bubble.role !== role || !bubble.text.includes(text))
          continue
        if (!/^[1-9]\d*$/u.test(bubble.sequence))
          throw new Error('A chat bubble requires a positive Worker sequence.')
        if (matching.has(bubble.sequence))
          throw new Error('The rendered chat window contains duplicate bubbles for one Worker sequence.')
        matching.add(bubble.sequence)
        sequences.add(bubble.sequence)
      }
      const end = Math.max(0, window.contentHeight - window.height)
      // Browsers round scroll offsets to physical pixels at the end of the list.
      if (end - window.top <= 1)
        return sequences.size
      const position = [
        window.top,
        window.height,
        window.contentHeight,
        ...window.bubbles.map(bubble => bubble.sequence),
      ].join(':')
      if (visited.has(position))
        throw new Error('The chat viewport did not advance through the virtual transcript.')
      visited.add(position)
      await viewport.scrollTo(Math.min(end, window.top + Math.max(1, window.height * 0.8)))
    }
  }, async () => {
    if (atEnd)
      await viewport.scrollToEnd()
    else
      await viewport.scrollTo(initial.top)
  })
}

/** Count visible bubbles across the full transcript, then restore the current scroll position. */
export async function countChatBubbles(page: Page, role: 'user' | 'agent', text: string, originalUserSequence: bigint): Promise<number> {
  if (originalUserSequence < 1n)
    throw new Error('A chat transcript count requires a positive original Worker sequence.')
  const container = chatScrollContainer(page)
  return countMatchingChatBubbles({
    reveal: async () => scrollChatRowIntoView(page, originalUserSequence),
    scrollToEnd: async () => {
      await container.evaluate((element) => {
        element.scrollTop = element.scrollHeight
      })
    },
    read: async () => {
      const geometry = await container.evaluate(async (element) => {
        // Let scroll handlers and the virtualizer render before reading this window.
        await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
        await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
        return { top: element.scrollTop, height: element.clientHeight, contentHeight: element.scrollHeight }
      })
      const bubbles = await readAttached(container.locator('[data-testid="message-bubble"]:visible'), 'the rendered chat bubbles', (elements) => {
        if (elements.some(element => !element.isConnected))
          return null
        return elements.map(element => ({
          sequence: element.closest('[data-seq]')?.getAttribute('data-seq') ?? '',
          role: element.getAttribute('data-role') ?? '',
          text: element.textContent ?? '',
        }))
      })
      return { ...geometry, bubbles }
    },
    scrollTo: async (top) => {
      await container.evaluate((element, value) => {
        element.scrollTop = value
      }, top)
    },
  }, role, text)
}
