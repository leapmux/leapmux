export interface FileTreeOps {
  refresh: () => void
  toggleHiddenFiles: () => void
}

const dialogOpsStack: FileTreeOps[] = []
const sidebarOpsStack: FileTreeOps[] = []

function unregister(stack: FileTreeOps[], ops: FileTreeOps): void {
  const idx = stack.lastIndexOf(ops)
  if (idx >= 0)
    stack.splice(idx, 1)
}

function activeOps(): FileTreeOps | undefined {
  return dialogOpsStack.at(-1) ?? sidebarOpsStack.at(-1)
}

export function registerDialogFileTreeOps(ops: FileTreeOps): () => void {
  dialogOpsStack.push(ops)
  return () => unregister(dialogOpsStack, ops)
}

export function registerSidebarFileTreeOps(ops: FileTreeOps): () => void {
  sidebarOpsStack.push(ops)
  return () => unregister(sidebarOpsStack, ops)
}

export function refreshFileTree(): void {
  activeOps()?.refresh()
}

export function toggleHiddenFiles(): void {
  activeOps()?.toggleHiddenFiles()
}
