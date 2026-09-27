import { describe, expect, it } from 'vitest'
import type { FileEntry } from '@/api/types'
import { jsonErrorOffset, lineOf, managedLines, problemsIn } from './code-lint'
import { baseName, crumbs, fileKind, inside, inWorld, isPlayerList, joinPath, languageOf, nameProblem, opensAsText, parentOf, pickedFiles, sortEntries, topNames } from './files'

const entry = (name: string, type: FileEntry['type'] = 'file', size = 0, modifiedAt = '2026-09-20T10:00:00Z'): FileEntry => ({ name, type, size, modifiedAt })

describe('file paths', () => {
  it('names a path’s folder, its name and each folder on the way', () => {
    expect(parentOf('plugins/LuckPerms/config.yml')).toBe('plugins/LuckPerms')
    expect(parentOf('server.properties')).toBe('')
    expect(baseName('plugins/LuckPerms/config.yml')).toBe('config.yml')
    expect(joinPath('', 'eula.txt')).toBe('eula.txt')
    expect(joinPath('plugins', 'Chunky')).toBe('plugins/Chunky')
    expect(crumbs('plugins/LuckPerms/config.yml')).toEqual([
      { name: 'plugins', path: 'plugins' },
      { name: 'LuckPerms', path: 'plugins/LuckPerms' },
      { name: 'config.yml', path: 'plugins/LuckPerms/config.yml' },
    ])
    expect(crumbs('')).toEqual([])
  })

  it('knows the world folders and what’s in them, and not a folder that only starts with the name', () => {
    const worlds = ['world', 'world_nether', 'world_the_end']
    expect(inWorld('world', worlds)).toBe(true)
    expect(inWorld('world/region/r.0.0.mca', worlds)).toBe(true)
    expect(inWorld('world_nether/DIM-1', worlds)).toBe(true)
    expect(inWorld('world-backup/level.dat', worlds)).toBe(false)
    expect(inWorld('plugins/world', worlds)).toBe(false)
  })

  it('won’t move a folder into itself', () => {
    expect(inside('plugins', 'plugins')).toBe(true)
    expect(inside('plugins/Chunky', 'plugins')).toBe(true)
    expect(inside('plugins-old', 'plugins')).toBe(false)
    expect(inside('', 'plugins')).toBe(false)
  })

  it('keeps the folders of a picked folder in each file’s path', () => {
    const file = new File(['x'], 'config.yml')
    Object.defineProperty(file, 'webkitRelativePath', { value: 'Essentials/config.yml' })
    const picked = pickedFiles([file, new File(['y'], 'eula.txt')])
    expect(picked.map((p) => p.path)).toEqual(['Essentials/config.yml', 'eula.txt'])
    expect(topNames(picked)).toEqual(['Essentials', 'eula.txt'])
  })
})

describe('file names', () => {
  it('refuses names the agent would refuse, and ones in use', () => {
    expect(nameProblem('')).toBe('files.name.empty')
    expect(nameProblem('   ')).toBe('files.name.empty')
    expect(nameProblem('a/b')).toBe('files.name.slash')
    expect(nameProblem('a\\b')).toBe('files.name.slash')
    expect(nameProblem('..')).toBe('files.name.dots')
    expect(nameProblem('.')).toBe('files.name.dots')
    expect(nameProblem('bell\u0007')).toBe('files.name.control')
    expect(nameProblem('é'.repeat(128))).toBe('files.name.long')
    expect(nameProblem('plugins', ['plugins', 'world'])).toBe('files.name.taken')
    expect(nameProblem('.rcon-cli.env')).toBeUndefined()
    expect(nameProblem('My Plugin (old).jar', ['plugins'])).toBeUndefined()
  })
})

describe('file kinds', () => {
  it('opens text in the editor and offers anything else as a download', () => {
    expect(fileKind(entry('plugins', 'folder'))).toBe('folder')
    expect(fileKind(entry('evil', 'link'))).toBe('link')
    expect(fileKind(entry('pipe', 'special'))).toBe('special')
    expect(fileKind(entry('LuckPerms.jar'))).toBe('archive')
    expect(fileKind(entry('2026-09-26-1.log.gz'))).toBe('archive')
    expect(fileKind(entry('server-icon.png'))).toBe('image')
    expect(fileKind(entry('r.0.0.mca'))).toBe('binary')
    expect(fileKind(entry('level.dat_old'))).toBe('binary')
    for (const name of ['server.properties', 'bukkit.yml', 'ops.json', 'lithium.toml', 'core.conf', 'eula.txt', 'latest.log', '.rcon-cli.env', 'README']) {
      expect(opensAsText(entry(name)), name).toBe(true)
    }
    expect(opensAsText(entry('world', 'folder'))).toBe(false)
  })

  it('picks the editor’s language by the file’s name', () => {
    expect(languageOf('server.properties')).toBe('properties')
    expect(languageOf('paper-global.yml')).toBe('yaml')
    expect(languageOf('config.YAML')).toBe('yaml')
    expect(languageOf('ops.json')).toBe('json')
    expect(languageOf('pack.mcmeta')).toBe('json')
    expect(languageOf('client.json5')).toBe('json5')
    expect(languageOf('fabric.toml')).toBe('toml')
    expect(languageOf('core.conf')).toBe('ini')
    expect(languageOf('mod.cfg')).toBe('ini')
    expect(languageOf('start.sh')).toBe('shell')
    expect(languageOf('script.zs')).toBe('javascript')
    expect(languageOf('eula.txt')).toBe('plain')
    expect(languageOf('.env')).toBe('plain')
  })

  it('knows the lists the server writes over while it runs', () => {
    expect(isPlayerList('whitelist.json')).toBe(true)
    expect(isPlayerList('ops.json')).toBe(true)
    expect(isPlayerList('plugins/whitelist.json')).toBe(false)
  })
})

describe('sorting a folder', () => {
  const list = [entry('b.yml', 'file', 10, '2026-09-21T00:00:00Z'), entry('world', 'folder'), entry('A.yml', 'file', 30, '2026-09-19T00:00:00Z'), entry('r.10.0.mca', 'file', 20), entry('r.9.0.mca', 'file', 5), entry('config', 'folder')]
  it('puts folders first, and names as people read them', () => {
    expect(sortEntries(list).map((e) => e.name)).toEqual(['config', 'world', 'A.yml', 'b.yml', 'r.9.0.mca', 'r.10.0.mca'])
    expect(sortEntries(list, 'name', true).map((e) => e.name)).toEqual(['world', 'config', 'r.10.0.mca', 'r.9.0.mca', 'b.yml', 'A.yml'])
  })
  it('sorts by size and by when it changed, folders still first', () => {
    expect(sortEntries(list, 'size').map((e) => e.name)).toEqual(['config', 'world', 'r.9.0.mca', 'b.yml', 'r.10.0.mca', 'A.yml'])
    expect(sortEntries(list, 'modified', true).map((e) => e.name).slice(2, 4)).toEqual(['b.yml', 'r.10.0.mca'])
  })
})

describe('checking a file before it’s saved', () => {
  it('finds where JSON stops parsing', () => {
    const text = '[\n  {"name": "JunoFox",}\n]\n'
    const [p] = problemsIn(text, 'json')
    expect(p?.message).toBe('files.editor.jsonProblem')
    expect(lineOf(text, p?.from ?? 0)).toBe(2)
    expect(problemsIn('[{"name": "JunoFox"}]\n', 'json')).toEqual([])
    expect(problemsIn('   \n', 'json')).toEqual([])
    expect(jsonErrorOffset('{"a": }', 'Unexpected token } in JSON at position 6')).toBe(6)
    expect(jsonErrorOffset('{\n"a": }', 'Expected value at line 2 column 6')).toBe(7)
  })

  it('finds YAML indented with a tab, and YAML that doesn’t read', () => {
    const tabbed = 'settings:\n\tallow-end: true\n'
    const [tab] = problemsIn(tabbed, 'yaml')
    expect(tab?.message).toBe('files.editor.yamlTab')
    expect(lineOf(tabbed, tab?.from ?? 0)).toBe(2)
    expect(problemsIn('settings:\n  allow-end: true\n  warn-on-overload: [true\n', 'yaml')[0]?.message).toBe('files.editor.yamlProblem')
    expect(problemsIn('settings:\n  allow-end: true\n', 'yaml')).toEqual([])
  })

  it('leaves the kinds it can’t check alone', () => {
    expect(problemsIn('motd=\\u00a7aHi\n{', 'properties')).toEqual([])
    expect(problemsIn('[unclosed', 'toml')).toEqual([])
  })

  it('marks the keys of server.properties Playkeeper sets, not comments or other keys', () => {
    const text = '#motd=Old\nmotd=Survival\nmax-players = 10\nview-distance=10\n  pvp:false\n'
    const lines = managedLines(text, ['motd', 'max-players', 'pvp'])
    expect(lines.map((l) => [l.key, text.slice(l.from, l.to), lineOf(text, l.from)])).toEqual([
      ['motd', 'motd', 2],
      ['max-players', 'max-players', 3],
      ['pvp', 'pvp', 5],
    ])
    expect(managedLines(text, [])).toEqual([])
  })
})
