import { describe, expect, it } from 'vitest'
import { analyzeCopyTargets } from './copyTargets.js'

function ofKind(targets: ReturnType<typeof analyzeCopyTargets>, kind: 'code' | 'quote') {
  return targets.filter((t) => t.kind === kind)
}

/**
 * Parity suite — these cases mirror internal/tui/copy_targets_test.go so the
 * TUI /copy picker and the Web/Desktop copy menu extract byte-identical
 * targets from the same response markdown.
 */
describe('analyzeCopyTargets', () => {
  it('returns no targets for empty or whitespace-only input', () => {
    expect(analyzeCopyTargets('')).toEqual([])
    expect(analyzeCopyTargets('   \n  \n')).toEqual([])
  })

  it('yields only the full-response target for plain prose', () => {
    const md = 'Just prose.\nSecond line, no fences or quotes.\n'
    const targets = analyzeCopyTargets(md)
    expect(targets).toHaveLength(1)
    expect(targets[0].kind).toBe('full')
    expect(targets[0].label).toBe('Full response')
    expect(targets[0].text).toBe(md.trim())
  })

  it('extracts code blocks without fence or info-string chrome', () => {
    const md = [
      'Intro text.',
      '```go title=main.go',
      'package main',
      '',
      'func main() {}',
      '```',
      'middle',
      '~~~python:script.py',
      "print('hi')",
      '~~~~',
      'end',
    ].join('\n')

    const targets = analyzeCopyTargets(md)
    const codes = ofKind(targets, 'code')
    expect(codes).toHaveLength(2)

    expect(codes[0].index).toBe(1)
    expect(codes[0].lang).toBe('go')
    expect(codes[0].filename).toBe('main.go')
    expect(codes[0].text).toBe('package main\n\nfunc main() {}')

    expect(codes[1].index).toBe(2)
    expect(codes[1].lang).toBe('python')
    expect(codes[1].filename).toBe('script.py')
    expect(codes[1].text).toBe("print('hi')")

    // Full response keeps everything, fences included.
    const full = targets.find((t) => t.kind === 'full')
    expect(full?.text).toContain('```go title=main.go')
  })

  it('keeps a still-streaming unterminated fence copyable', () => {
    const md = 'Header\n```go\nfunc A() {\n\treturn 1'
    const codes = ofKind(analyzeCopyTargets(md), 'code')
    expect(codes).toHaveLength(1)
    expect(codes[0].text).toBe('func A() {\n\treturn 1')
    expect(codes[0].lang).toBe('go')
  })

  it('strips the opener indentation from indented fences', () => {
    const md = '  ```js\n  let a = 1\n      deeper\n  ```'
    const codes = ofKind(analyzeCopyTargets(md), 'code')
    expect(codes[0].text).toBe('let a = 1\n    deeper')
  })

  it('groups consecutive blockquote lines and splits on blank lines', () => {
    const md = [
      '> first quote line',
      '>second no space',
      '>',
      '> continued after empty marker',
      '',
      'plain paragraph',
      '',
      '> second group',
      '> 日本語の引用',
    ].join('\n')

    const quotes = ofKind(analyzeCopyTargets(md), 'quote')
    expect(quotes).toHaveLength(2)
    expect(quotes[0].text).toBe('first quote line\nsecond no space\n\ncontinued after empty marker')
    expect(quotes[1].text).toBe('second group\n日本語の引用')
    expect(quotes.map((q) => q.index)).toEqual([1, 2])
  })

  it('treats blockquotes inside a code fence as code', () => {
    const md = '```\n> not a quote\n```\n> real quote'
    const targets = analyzeCopyTargets(md)
    expect(ofKind(targets, 'code')).toHaveLength(1)
    expect(ofKind(targets, 'code')[0].text).toBe('> not a quote')
    expect(ofKind(targets, 'quote')).toHaveLength(1)
    expect(ofKind(targets, 'quote')[0].text).toBe('real quote')
  })

  it('normalizes CRLF input before extracting', () => {
    const md = 'line\r\n```go\r\ncode()\r\n```\r\n> quote\r\n'
    const targets = analyzeCopyTargets(md)
    expect(ofKind(targets, 'code')[0].text).toBe('code()')
    expect(ofKind(targets, 'quote')[0].text).toBe('quote')
  })

  it('numbers kinds independently and preserves document order', () => {
    const md = '> q1\n```js\na()\n```\n> q2\n```py\nb()\n```'
    const targets = analyzeCopyTargets(md)
    expect(ofKind(targets, 'code').map((t) => t.text)).toEqual(['a()', 'b()'])
    expect(ofKind(targets, 'quote').map((t) => t.text)).toEqual(['q1', 'q2'])
  })
})
