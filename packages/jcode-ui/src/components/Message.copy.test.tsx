import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Message as MessageData } from 'jcode-ui-core'
import { RuntimeProvider, createMockRuntime } from 'jcode-ui-core/runtime'
import { Message } from './Message.js'

// jsdom ships no clipboard implementation — install a controllable one.
function installClipboard() {
  const writeText = vi.fn((_: string) => Promise.resolve())
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText },
    configurable: true,
  })
  return writeText
}

beforeEach(() => {
  installClipboard()
})

afterEach(cleanup)

function assistantMessage(content: string): MessageData {
  return { id: 'a1', role: 'assistant', content, timestamp: 1_000 }
}

function renderMessage(content: string) {
  return render(
    <RuntimeProvider runtime={createMockRuntime()}>
      <Message message={assistantMessage(content)} />
    </RuntimeProvider>,
  )
}

describe('Message copy targets', () => {
  it('opens the unified target menu for responses with code blocks and quotes', () => {
    renderMessage('Intro\n```go title=main.go\nfunc A() {}\n```\n> a quote\n')

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))

    const menu = screen.getByRole('menu', { name: 'Copy targets' })
    expect(screen.getByText('Full response')).toBeTruthy()
    expect(screen.getByText('Code block 1')).toBeTruthy()
    expect(screen.getByText('Blockquote 1')).toBeTruthy()
    expect(menu.getAttribute('role')).toBe('menu')
  })

  it('copies the selected code block without fence or language chrome', async () => {
    renderMessage('Intro\n```go\nfunc A() {}\n```\n')

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    fireEvent.click(screen.getByRole('menuitem', { name: /Code block 1/ }))

    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalled())
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('func A() {}')
  })

  it('copies plain messages directly without opening a menu', async () => {
    renderMessage('All checks passed.')

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))

    expect(screen.queryByRole('menu')).toBeNull()
    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalled())
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('All checks passed.')
  })

  it('reports a denied clipboard as a failure instead of success', async () => {
    const writeText = installClipboard()
    writeText.mockRejectedValue(new Error('denied'))

    renderMessage('Plain text')

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))

    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Copy failed' })).toBeTruthy(),
    )
    // The legacy execCommand fallback also fails under jsdom (undefined), so
    // the outcome must be a visible failure, never a fake "Copied".
    expect(screen.queryByRole('button', { name: 'Copied' })).toBeNull()
  })

  it('closes the menu on Escape and backdrop click without copying', () => {
    renderMessage('Intro\n```go\nx()\n```\n')

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByRole('menu')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    fireEvent.click(screen.getByRole('button', { name: 'Close copy menu' }))
    expect(screen.queryByRole('menu')).toBeNull()
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
  })
})
