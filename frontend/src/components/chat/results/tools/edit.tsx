import FileEdit from 'lucide-solid/icons/file-pen'
import { fileChangeRenderer } from './fileChanges'

export const editRenderer = fileChangeRenderer<'edit'>({
  icon: FileEdit,
  label: 'Edit',
})
