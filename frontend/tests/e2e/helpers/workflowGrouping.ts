import type { Locator } from '@playwright/test'

/** Find the group heading before a row, past other rows in that group. */
export function workflowGroupHeadingElement(row: Element): Element | null {
  let sibling = row.previousElementSibling
  while (sibling?.getAttribute('data-testid') === 'bg-task-row')
    sibling = sibling.previousElementSibling
  return sibling
}

/** Read the group heading above a background task row. */
export async function workflowGroupHeading(row: Locator): Promise<string> {
  const heading = await row.evaluateHandle(workflowGroupHeadingElement)
  try {
    return await heading.evaluate(element => element?.textContent?.trim() ?? '')
  }
  finally {
    await heading.dispose()
  }
}

/** Check that two rows follow the same group heading element. */
export async function workflowRowsShareGroup(first: Locator, second: Locator): Promise<boolean> {
  const firstHeading = await first.evaluateHandle(workflowGroupHeadingElement)
  try {
    const secondHeading = await second.evaluateHandle(workflowGroupHeadingElement)
    try {
      return await firstHeading.evaluate((element, other) => element !== null && element.isSameNode(other), secondHeading)
    }
    finally {
      await secondHeading.dispose()
    }
  }
  finally {
    await firstHeading.dispose()
  }
}
