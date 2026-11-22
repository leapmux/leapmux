/** Build a directory `FileInfo`-shaped fixture for tree component unit tests. */
export function makeDirFixture(path: string, name: string, hidden = false) {
  return {
    name,
    path,
    isDir: true,
    hidden,
    size: 0n,
    modTime: '2026-01-01T00:00:00Z',
  }
}

/** Find a rendered tree row element by its visible name text. */
export function findTreeRowByName(name: string): HTMLElement | undefined {
  return [
    ...document.querySelectorAll<HTMLElement>('[data-testid="tree-row"]'),
  ].find(el => el.querySelector('[data-testid="tree-row-name"]')?.textContent === name)
}
