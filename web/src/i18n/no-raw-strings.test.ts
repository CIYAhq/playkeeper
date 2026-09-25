import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'
import { expect, it } from 'vitest'

// Every word a person reads goes through t(). This walks every component and
// fails on text written straight into JSX or into attributes people read.
const src = fileURLToPath(new URL('..', import.meta.url))
const readAttributes = new Set(['aria-label', 'aria-description', 'aria-valuetext', 'title', 'placeholder', 'alt', 'label'])
const letters = /\p{L}/u

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return files(path)
    return path.endsWith('.tsx') && !path.endsWith('.test.tsx') ? [path] : []
  })
}

function rawStrings(path: string): string[] {
  const file = ts.createSourceFile(path, readFileSync(path, 'utf8'), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const found: string[] = []
  const where = (node: ts.Node) => `${relative(src, path)}:${file.getLineAndCharacterOfPosition(node.getStart()).line + 1}`
  const isText = (node: ts.Node | undefined) => !!node && (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) && letters.test(node.text)
  const visit = (node: ts.Node) => {
    if (ts.isJsxText(node) && letters.test(node.text)) found.push(`${where(node)} text "${node.text.trim()}"`)
    if (ts.isJsxExpression(node) && (ts.isJsxElement(node.parent) || ts.isJsxFragment(node.parent)) && isText(node.expression)) found.push(`${where(node)} text ${node.expression?.getText()}`)
    if (ts.isJsxAttribute(node) && readAttributes.has(node.name.getText())) {
      const init = node.initializer
      const value = init && ts.isJsxExpression(init) ? init.expression : init
      if (isText(value)) found.push(`${where(node)} ${node.name.getText()}=${value?.getText()}`)
    }
    ts.forEachChild(node, visit)
  }
  visit(file)
  return found
}

it('routes every UI string through the translation layer', () => {
  const all = files(src)
  expect(all.length).toBeGreaterThan(40)
  expect(all.flatMap(rawStrings)).toEqual([])
})

it('catches raw strings', () => {
  const sample = join(src, 'i18n', '__sample__.tsx')
  const scan = (code: string) => {
    const file = ts.createSourceFile(sample, code, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
    let n = 0
    const visit = (node: ts.Node) => {
      if (ts.isJsxText(node) && letters.test(node.text)) n++
      if (ts.isJsxAttribute(node) && readAttributes.has(node.name.getText()) && node.initializer && ts.isStringLiteral(node.initializer) && letters.test(node.initializer.text)) n++
      ts.forEachChild(node, visit)
    }
    visit(file)
    return n
  }
  expect(scan('const a = <p>Hello</p>')).toBe(1)
  expect(scan('const a = <input placeholder="Name" />')).toBe(1)
  expect(scan("const a = <p className=\"x\">{t('brand.name')}</p>")).toBe(0)
})
