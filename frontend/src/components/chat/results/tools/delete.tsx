import FileX from 'lucide-solid/icons/file-x'
import { fileChangeRenderer } from './fileChanges'

export const deleteRenderer = fileChangeRenderer<'delete'>({
  icon: FileX,
  label: 'Delete',
})
