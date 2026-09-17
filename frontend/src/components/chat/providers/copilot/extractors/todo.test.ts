import { describe, expect, it } from 'vitest'
import corpus from '../../../../../../../testdata/copilot_checklist_conformance.json'
import { copilotChecklistItems } from './todo'

// The Go worker and this plugin both read Copilot's markdown checklist, so one corpus
// is the executable specification that each side replays.
describe('copilotChecklistItems conformance', () => {
  for (const testCase of corpus.cases) {
    it(testCase.name, () => {
      const items = copilotChecklistItems(testCase.checklist)
      expect(items.map(item => ({ content: item.content, status: item.status })))
        .toEqual(testCase.expected)
      for (const item of items)
        expect(item.activeForm).toBe(item.content)
    })
  }
})
