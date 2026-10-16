import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { racyChatReads } from '~/test-support/chatRowReads'
import { collectE2EFiles } from '~/test-support/e2eFiles'
import { frontendRoot, posixRelative } from '~/test-support/sourceTree'

// E2E guard: read a chat row through `readAttached`, never through a bare
// `locator.evaluate` / `locator.evaluateAll`.
//
// Resolving a locator and reading from what it resolved are two browser round
// trips, and a chat row does not always survive the gap. ChatView re-creates a
// row when a notification changes its sequence. A resolve just before the swap leaves the read
// holding a DETACHED node.
//
// `evaluateAll` is no safer than `evaluate`, which is worth stating because it
// reads as though it should be: Playwright runs it as `querySelectorAll` into an
// array HANDLE and then a second call that applies the callback to that handle,
// so the elements are pinned a round trip before the callback sees them. A probe
// that replaced its element on every event-loop turn fed both forms a detached
// node on most attempts.
//
// What makes this worth a guard rather than a comment is how quietly it fails. A
// detached node reports an empty rect, a null `closest()` and NO computed style,
// so a geometry assertion fails at random (issue 402) while a colour assertion of
// the "these two must differ" shape PASSES against an empty string -- green, and
// proving nothing. 033 held one of those.
//
// `readAttached` in tests/e2e/helpers/ui.ts takes the read that the guard
// requires: it hands the callback every match, the callback keeps only what is
// still in the document, and returning null retries.
//
// Only CHAT locators are in scope. The scroll container, the composer, a popover
// and the sidebar are not rebuilt under a reader, so `locator.evaluate` stays
// correct for them.

describe('racyChatReads', () => {
  it('keeps locator bindings inside their own function scope', () => {
    const source = `
      function readChat(page) {
        const menu = userBubbles(page)
        return readAttached(menu, 'chat', nodes => nodes.length)
      }
      function readMenu(page) {
        const menu = page.getByRole('menu')
        return menu.evaluateAll(nodes => nodes.length)
      }
    `
    expect(racyChatReads(source)).toEqual([])
  })

  it('does not classify browser DOM nodes as Playwright locators', () => {
    const source = `
      function reveal(page) {
        const container = chatScrollContainer(page)
        return container.evaluate(element => {
          const menu = element.querySelector('[data-seq="7"]')
          menu.scrollIntoView()
        })
      }
      function readMenu(page) {
        const menu = page.getByRole('menu')
        return menu.evaluateAll(nodes => nodes.length)
      }
    `
    expect(racyChatReads(source)).toEqual([])
  })

  it('still rejects direct and aliased chat reads across nested callbacks', () => {
    const source = `
      function inspect(page) {
        const bubble = page.locator('[data-testid="message-bubble"]:visible')
        const alias = bubble.locator('pre')
        const check = () => alias.evaluate(node => node.textContent)
        assistantBubbles(page).evaluateAll(nodes => nodes.length)
        return check
      }
    `
    expect(racyChatReads(source)).toHaveLength(2)
  })

  it('follows imported aliases and selector aliases across block scopes', () => {
    const source = `
      import { assistantBubbles as answers } from './ui'
      const selector = '[data-testid="message-content"]'
      function inspect(page) {
        const row = answers(page)
        { const row = page.getByRole('menu'); row.evaluateAll(nodes => nodes.length) }
        const nested = () => row.locator('pre').evaluate(node => node.textContent)
        page.locator(selector)['evaluateAll'](nodes => nodes.length)
      }
    `
    expect(racyChatReads(source)).toHaveLength(2)
  })

  it('does not treat a locator page or count as another locator', () => {
    expect(racyChatReads(`
      const bubble = assistantBubbles(page)
      const owner = bubble.page()
      owner.evaluate(() => document.title)
    `)).toEqual([])
  })

  it('handles cyclic aliases without recursion and ignores comments and quoted code', () => {
    expect(racyChatReads(`
      const first = second
      const second = first
      first.evaluate(() => 1)
      // assistantBubbles(page).evaluate(() => 2)
      const example = 'messageBubbles(page).evaluate(() => 3)'
    `)).toEqual([])
  })
})

describe('e2e chat row reads', () => {
  it('never reads a chat row through a two-round-trip evaluate', () => {
    const offenders = new Set<string>()
    for (const file of collectE2EFiles()) {
      const source = readFileSync(file, 'utf-8')
      for (const { index, text } of racyChatReads(source, file)) {
        const line = source.slice(0, index).split('\n').length
        offenders.add(`${posixRelative(frontendRoot, file)}:${line}  ${text}`)
      }
    }
    const hint = [
      'A chat row is re-created whenever its entry is replaced, so a handle resolved one round trip',
      'earlier can already be detached -- which reports an empty rect and NO computed style.',
      '`evaluateAll` pins its elements a round trip early too. Read through readAttached',
      '(tests/e2e/helpers/ui.ts), skipping any match whose `isConnected` is false, or measure the row',
      'with measureBubbleEdges / measureAgainstChatList:',
    ].join(' ')
    expect([...offenders].sort(), `${hint}\n  ${[...offenders].sort().join('\n  ')}`).toEqual([])
  })
})
