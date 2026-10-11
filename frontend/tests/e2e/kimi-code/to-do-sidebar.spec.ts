import { expect } from '@playwright/test'
import { exerciseTodoListReplacement } from '../helpers/todoSidebar'
import { chatScrollContainer } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('tracks the Kimi Code to-do list', () => {
  // Each TodoList call states the whole list, so the second call replaces the
  // first rather than adding to it.
  kimiTest('the sidebar follows each list the agent writes, and keeps it after a reload', async ({ native }) => {
    await exerciseTodoListReplacement(native, {
      afterFirstList: async () => {
        // The chat draws the call as the list it wrote.
        const chat = chatScrollContainer(native.page)
        await expect(chat.getByText('3 tasks', { exact: true })).toBeVisible()
        await expect(chat).toContainText('List three checks')
      },
    })
  })
})
