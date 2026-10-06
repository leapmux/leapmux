import type { Page } from '@playwright/test'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { allowReadIfAsked } from './readPermission'

/** The fake banner of one test: whether it shows, its text, and each click of its Allow button. */
const banner = vi.hoisted(() => ({ visible: false, text: '', clicks: [] as string[] }))

/** A probe of the fake banner that the fake `expect` reads. */
interface BannerProbe {
  bannerProbe: true
  isVisible: () => Promise<boolean>
}

function isBannerProbe(value: unknown): value is BannerProbe {
  return typeof value === 'object' && value !== null && 'bannerProbe' in value
}

vi.mock('../helpers/ui', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/ui')>(),
  controlBanner: (): BannerProbe => ({ bannerProbe: true, isVisible: async () => banner.visible }),
  answerControl: async (_page: Page, decision: string) => {
    banner.clicks.push(decision)
  },
}))

// A fake poll reads once, and a fake banner assertion reads the banner text once.
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown, message?: string) => {
    if (isBannerProbe(value))
      return { toContainText: async (text: string) => expect(banner.text, message).toContain(text) }
    return expect(value, message)
  }
  return {
    ...actual,
    expect: Object.assign(check, {
      poll: (read: () => Promise<unknown>, options?: { message?: string }) => ({
        not: { toBe: async (unexpected: unknown) => expect(await read(), options?.message).not.toBe(unexpected) },
      }),
    }),
  }
})

const page = {} as Page

describe('allowReadIfAsked', () => {
  beforeEach(() => {
    banner.visible = false
    banner.text = ''
    banner.clicks = []
  })

  it('answers nothing when the child goes on without a request', async () => {
    await allowReadIfAsked(page, async () => true)
    expect(banner.clicks).toEqual([])
  })

  it('allows the read when Fast Agent asks for it', async () => {
    banner.visible = true
    banner.text = 'Permission Required read_text_file /project/child-note.txt'
    await allowReadIfAsked(page, async () => false)
    expect(banner.clicks).toEqual(['allow'])
  })

  it('refuses to allow a request for another tool', async () => {
    banner.visible = true
    banner.text = 'Permission Required write_text_file /project/child-note.txt'
    await expect(allowReadIfAsked(page, async () => false)).rejects.toThrow('read_text_file')
    expect(banner.clicks).toEqual([])
  })

  it('fails while the child neither goes on nor asks', async () => {
    await expect(allowReadIfAsked(page, async () => false)).rejects.toThrow('the Fast Agent child continues or asks to read its file')
    expect(banner.clicks).toEqual([])
  })
})
