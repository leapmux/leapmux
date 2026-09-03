import { describe, expect, it } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { tabTypeFromProtoName } from './api'

// A `.test.ts` under tests/e2e/ runs in `task test-frontend` -- no browser, no
// hub, milliseconds. The E2E suite never sees it.
describe('tabTypeFromProtoName', () => {
  // The regression this replaced a hand-written table for: the table listed
  // four names, IMAGE was missing, and `listTabsViaAPI` threw on any workspace
  // that held an image tab -- so a spec failed in teardown rather than in its
  // own assertion.
  it('answers every value the TabType enum declares', () => {
    expect(tabTypeFromProtoName('TAB_TYPE_UNSPECIFIED')).toBe(TabType.UNSPECIFIED)
    expect(tabTypeFromProtoName('TAB_TYPE_AGENT')).toBe(TabType.AGENT)
    expect(tabTypeFromProtoName('TAB_TYPE_TERMINAL')).toBe(TabType.TERMINAL)
    expect(tabTypeFromProtoName('TAB_TYPE_FILE')).toBe(TabType.FILE)
    expect(tabTypeFromProtoName('TAB_TYPE_IMAGE')).toBe(TabType.IMAGE)
  })

  // Throwing is the contract: a tab this helper cannot classify must not be
  // silently dropped from a cleanup sweep.
  it('throws for a name the enum does not declare, naming the tab', () => {
    expect(() => tabTypeFromProtoName('TAB_TYPE_BOGUS', 'tab-9'))
      .toThrow(/unrecognized tab_type "TAB_TYPE_BOGUS" for tab tab-9/)
    expect(() => tabTypeFromProtoName('')).toThrow(/unrecognized tab_type ""/)
  })
})
