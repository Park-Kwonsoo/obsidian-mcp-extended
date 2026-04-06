/**
 * Tool exports — thin re-export layer.
 *
 * Delegates to the filesystem backend.
 * When CLI backend + resolver are wired in (Phase 2.2+),
 * this module will route through the backend resolver instead.
 */

export {
  searchVault,
  searchByTitle,
  listNotes,
  readNote,
  writeNote,
  deleteNote,
  searchByTags,
  getNoteMetadata,
  discoverMocs,
  readSection,
  patchNote,
  toggleCheckbox,
  extractTags,
} from './backends/filesystem.js';
