import { forwardRef, useEffect, useImperativeHandle, useRef } from 'react'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { json } from '@codemirror/lang-json'
import { yaml } from '@codemirror/lang-yaml'
import { bracketMatching, HighlightStyle, indentOnInput, indentUnit, StreamLanguage, syntaxHighlighting } from '@codemirror/language'
import { javascript } from '@codemirror/legacy-modes/mode/javascript'
import { properties } from '@codemirror/legacy-modes/mode/properties'
import { shell } from '@codemirror/legacy-modes/mode/shell'
import { toml } from '@codemirror/legacy-modes/mode/toml'
import { forceLinting, linter, lintGutter, type Diagnostic } from '@codemirror/lint'
import { highlightSelectionMatches, search, searchKeymap } from '@codemirror/search'
import { Compartment, EditorSelection, EditorState, type Extension } from '@codemirror/state'
import { drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers } from '@codemirror/view'
import { tags } from '@lezer/highlight'
import { t } from '@/i18n'
import { lineOf, managedLines, problemsIn } from '@/lib/code-lint'
import type { Language } from '@/lib/files'
import { cn } from '@/lib/utils'

// The file browser's editor: CodeMirror, in a shadow root of its own. The
// panel's Content Security Policy allows only its own style files, and
// CodeMirror styles a document with a <style> element; in a shadow root it
// uses a constructed style sheet instead, which the policy leaves alone.

export interface EditorProblem {
  line: number
  message: string
}

export interface CodeEditorHandle {
  focus: () => void
  goToLine: (line: number) => void
}

function languageFor(l: Language): Extension {
  switch (l) {
    case 'yaml':
      return yaml()
    case 'json':
    case 'json5':
      return json()
    case 'properties':
    case 'ini':
      return StreamLanguage.define(properties)
    case 'toml':
      return StreamLanguage.define(toml)
    case 'shell':
      return StreamLanguage.define(shell)
    case 'javascript':
      return StreamLanguage.define(javascript)
    case 'plain':
      return []
    default: {
      const unreachable: never = l
      return unreachable
    }
  }
}

// Playkeeper's colors: warm neutrals, the brand's green for keys.
const highlight = HighlightStyle.define([
  { tag: [tags.propertyName, tags.definition(tags.propertyName), tags.definition(tags.variableName), tags.attributeName], color: '#166534' },
  { tag: [tags.string, tags.quote, tags.special(tags.string), tags.content], color: '#8a4b0c' },
  { tag: [tags.number, tags.bool, tags.null, tags.atom], color: '#6b3fb8' },
  { tag: [tags.comment, tags.lineComment, tags.blockComment], color: '#6d7266', fontStyle: 'italic' },
  { tag: [tags.heading, tags.labelName, tags.typeName], color: '#1d211c', fontWeight: '700' },
  { tag: [tags.keyword, tags.operatorKeyword, tags.controlKeyword], color: '#a3341a' },
  { tag: [tags.separator, tags.punctuation, tags.brace, tags.squareBracket], color: '#5c6157' },
  { tag: tags.invalid, color: '#b91c1c' },
])

const theme = EditorView.theme({
  '&': { height: '100%', fontSize: '13px', color: 'var(--foreground)', backgroundColor: 'var(--card)' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.65', overscrollBehavior: 'contain' },
  '.cm-content': { padding: '10px 0', caretColor: 'var(--primary)' },
  '.cm-line': { padding: '0 16px 0 12px' },
  '.cm-gutters': { backgroundColor: 'var(--warm)', color: 'var(--muted-foreground)', border: 'none', borderRight: '1px solid var(--border)' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 8px 0 14px', minWidth: '44px' },
  '.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--primary) 4%, transparent)' },
  '.cm-activeLineGutter': { backgroundColor: 'color-mix(in srgb, var(--primary) 9%, transparent)', color: 'var(--foreground)' },
  '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
    backgroundColor: 'color-mix(in srgb, var(--primary) 20%, transparent) !important',
  },
  '.cm-cursor, .cm-dropCursor': { borderLeft: '2px solid var(--primary)' },
  '.cm-matchingBracket': { backgroundColor: 'color-mix(in srgb, var(--primary) 16%, transparent)', outline: 'none' },
  '.cm-selectionMatch': { backgroundColor: 'color-mix(in srgb, var(--marigold) 35%, transparent)' },
  '.cm-searchMatch': { backgroundColor: 'color-mix(in srgb, var(--marigold) 45%, transparent)', outline: '1px solid color-mix(in srgb, var(--warning) 60%, transparent)' },
  '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'color-mix(in srgb, var(--primary) 25%, transparent)' },
  '.cm-panels': { backgroundColor: 'var(--warm)', color: 'var(--foreground)', fontFamily: 'var(--font-sans)', fontSize: '13px' },
  '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--border)' },
  '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--border)' },
  '.cm-panel.cm-search': { padding: '8px 36px 8px 12px', display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px' },
  '.cm-panel.cm-search br': { display: 'none' },
  '.cm-panel.cm-search label': { display: 'inline-flex', alignItems: 'center', gap: '4px', fontSize: '12px', color: 'var(--muted-foreground)' },
  '.cm-textfield': {
    height: '30px',
    padding: '0 10px',
    border: '1px solid var(--input)',
    borderRadius: '8px',
    backgroundColor: 'var(--background)',
    color: 'var(--foreground)',
    fontSize: '13px',
    fontFamily: 'var(--font-sans)',
    outline: 'none',
  },
  '.cm-textfield:focus': { borderColor: 'var(--ring)', boxShadow: '0 0 0 3px color-mix(in srgb, var(--ring) 35%, transparent)' },
  '.cm-button': {
    height: '30px',
    padding: '0 10px',
    border: '1px solid var(--input)',
    borderRadius: '8px',
    backgroundImage: 'none',
    backgroundColor: 'var(--background)',
    color: 'var(--foreground)',
    fontSize: '13px',
    fontFamily: 'var(--font-sans)',
    cursor: 'pointer',
  },
  '.cm-button:hover': { backgroundColor: 'var(--accent)' },
  '.cm-button:active': { backgroundImage: 'none', backgroundColor: 'var(--accent)' },
  '.cm-panel.cm-search input[type=checkbox]': {
    appearance: 'none',
    width: '15px',
    height: '15px',
    margin: '0',
    border: '1px solid var(--input)',
    borderRadius: '4px',
    backgroundColor: 'var(--background)',
    display: 'inline-grid',
    placeContent: 'center',
    cursor: 'pointer',
  },
  '.cm-panel.cm-search input[type=checkbox]:checked': { backgroundColor: 'var(--primary)', borderColor: 'var(--primary)' },
  '.cm-panel.cm-search input[type=checkbox]:checked::after': { content: '""', width: '8px', height: '4px', border: '2px solid white', borderTop: 'none', borderRight: 'none', transform: 'rotate(-45deg) translate(1px, -1px)' },
  '.cm-panel.cm-search [name=close]': { position: 'absolute', top: '8px', right: '8px', width: '26px', height: '26px', fontSize: '18px', color: 'var(--muted-foreground)', background: 'none', border: 'none', borderRadius: '6px', cursor: 'pointer' },
  '.cm-panel.cm-search [name=close]:hover': { backgroundColor: 'var(--accent)', color: 'var(--foreground)' },
  '.cm-tooltip': { border: '1px solid var(--border)', borderRadius: '10px', backgroundColor: 'var(--popover)', boxShadow: '0 12px 32px -8px rgba(29,33,28,.18), 0 2px 6px rgba(29,33,28,.06)', overflow: 'hidden' },
  '.cm-tooltip-lint': { fontFamily: 'var(--font-sans)', fontSize: '13px' },
  '.cm-diagnostic': { padding: '6px 10px', borderLeft: 'none' },
  '.cm-diagnostic-error': { color: 'var(--destructive-foreground)' },
  '.cm-diagnostic-info': { color: 'var(--muted-foreground)' },
  '.cm-lint-marker': { width: '8px', height: '8px', borderRadius: '9999px', content: 'none' },
  '.cm-lint-marker-error': { backgroundColor: 'var(--destructive)' },
  '.cm-lint-marker-info': { backgroundColor: 'var(--primary)' },
  '.cm-gutter-lint': { width: '14px' },
  '.cm-gutter-lint .cm-gutterElement': { display: 'flex', alignItems: 'center', justifyContent: 'center', padding: '0' },
  '.cm-lintRange-info': { backgroundImage: 'none', textDecoration: 'underline dotted color-mix(in srgb, var(--primary) 60%, transparent)', textUnderlineOffset: '3px' },
})

/** A phone's editor has larger text, as its other fields do. */
const phoneTheme = EditorView.theme({ '&': { fontSize: '14px' }, '.cm-lineNumbers .cm-gutterElement': { minWidth: '34px', padding: '0 6px 0 8px' }, '.cm-line': { padding: '0 12px 0 8px' } })

export const CodeEditor = forwardRef<
  CodeEditorHandle,
  {
    value: string
    language: Language
    readOnly: boolean
    label: string
    phone?: boolean
    /** Keys of server.properties Playkeeper sets at each start, marked where they're set. */
    managed?: string[]
    onChange: (text: string) => void
    onSave: () => void
    onCursor?: (line: number, column: number) => void
    onProblems?: (problems: EditorProblem[]) => void
    className?: string
  }
>(function CodeEditor({ value, language, readOnly, label, phone, managed, onChange, onSave, onCursor, onProblems, className }, ref) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView>(undefined)
  const editable = useRef(new Compartment())
  const handlers = useRef({ onChange, onSave, onCursor, onProblems })
  useEffect(() => {
    handlers.current = { onChange, onSave, onCursor, onProblems }
  })

  useImperativeHandle(ref, () => ({
    focus: () => view.current?.focus(),
    goToLine: (line: number) => {
      const v = view.current
      if (!v) return
      const at = v.state.doc.line(Math.min(Math.max(1, line), v.state.doc.lines)).from
      v.dispatch({ selection: EditorSelection.cursor(at), scrollIntoView: true })
      v.focus()
    },
  }))

  useEffect(() => {
    const el = host.current
    if (!el) return
    const root = el.shadowRoot ?? el.attachShadow({ mode: 'open' })
    const mount = document.createElement('div')
    mount.style.height = '100%'
    root.replaceChildren(mount)
    const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false
    const check = linter((v): Diagnostic[] => {
      const text = v.state.doc.toString()
      const found = problemsIn(text, language)
      handlers.current.onProblems?.(found.map((p) => ({ line: lineOf(text, p.from), message: t(p.message) })))
      const out: Diagnostic[] = found.map((p) => ({ from: p.from, to: p.to, severity: 'error', message: t(p.message) }))
      for (const m of managedLines(text, managed ?? [])) out.push({ from: m.from, to: m.to, severity: 'info', message: t('files.editor.managedLine') })
      return out
    }, { delay: 300 })
    const state = EditorState.create({
      doc: value,
      extensions: [
        lineNumbers(),
        highlightActiveLineGutter(),
        history(),
        drawSelection({ cursorBlinkRate: reduced ? 0 : 1200 }),
        indentOnInput(),
        bracketMatching(),
        highlightActiveLine(),
        highlightSelectionMatches(),
        search({ top: true }),
        indentUnit.of('  '),
        EditorState.tabSize.of(4),
        languageFor(language),
        syntaxHighlighting(highlight),
        check,
        lintGutter(),
        theme,
        phone ? phoneTheme : [],
        editable.current.of([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]),
        EditorView.contentAttributes.of({ 'aria-label': label, autocapitalize: 'off', autocorrect: 'off', spellcheck: 'false' }),
        keymap.of([
          {
            key: 'Mod-s',
            preventDefault: true,
            run: () => {
              handlers.current.onSave()
              return true
            },
          },
          indentWithTab,
          ...defaultKeymap,
          ...historyKeymap,
          ...searchKeymap,
        ]),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) handlers.current.onChange(u.state.doc.toString())
          if (u.docChanged || u.selectionSet) {
            const head = u.state.selection.main.head
            const line = u.state.doc.lineAt(head)
            handlers.current.onCursor?.(line.number, head - line.from + 1)
          }
        }),
      ],
    })
    view.current = new EditorView({ state, parent: mount, root })
    // The marks and problems show as the file opens; edits are checked after a pause.
    forceLinting(view.current)
    return () => {
      view.current?.destroy()
      view.current = undefined
    }
    // A new file, language or set of managed keys makes a new editor; the text changes only inside it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [language, (managed ?? []).join(' '), label, phone])

  useEffect(() => {
    view.current?.dispatch({ effects: editable.current.reconfigure([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]) })
  }, [readOnly])

  // value is the text loaded into the editor: the file as opened, or as opened again.
  useEffect(() => {
    const v = view.current
    if (v && v.state.doc.toString() !== value) v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
  }, [value])

  return <div ref={host} className={cn('h-full min-h-0', className)} data-slot="code-editor" />
})
