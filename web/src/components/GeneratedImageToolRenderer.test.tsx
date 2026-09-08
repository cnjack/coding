import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Provider } from 'react-redux'
import type { ToolRendererProps } from 'jcode-ui-core/adapters'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { chatActions, sessionActions, store } from '../app/store'
import { i18n } from '../i18n'
import {
  GeneratedImageToolRenderer,
  shouldHideGeneratedImageCard,
} from './GeneratedImageToolRenderer'

const mocks = vi.hoisted(() => ({
  desktop: false,
  artifactContent: vi.fn(),
  openArtifact: vi.fn(),
}))

vi.mock('../lib/api', () => ({
  api: {
    artifactContent: mocks.artifactContent,
    openArtifact: mocks.openArtifact,
  },
}))

vi.mock('../lib/useDesktop', () => ({
  get isTauri() { return mocks.desktop },
}))

const imageArgs = '{"prompt":"a blue basketball"}'

function renderImage(overrides: Partial<ToolRendererProps> = {}) {
  const props: ToolRendererProps = {
    name: 'generate_image',
    args: imageArgs,
    status: 'running',
    phase: 'queued',
    startedAt: 100,
    ...overrides,
  }
  return render(
    <Provider store={store}>
      <GeneratedImageToolRenderer {...props} />
    </Provider>,
  )
}

beforeEach(async () => {
  cleanup()
  mocks.desktop = false
  mocks.artifactContent.mockReset()
  mocks.openArtifact.mockReset()
  mocks.artifactContent.mockResolvedValue(new Blob(['png'], { type: 'image/png' }))
  mocks.openArtifact.mockResolvedValue(undefined)
  Object.defineProperty(URL, 'createObjectURL', {
    configurable: true,
    value: vi.fn(() => 'blob:generated-image'),
  })
  Object.defineProperty(URL, 'revokeObjectURL', {
    configurable: true,
    value: vi.fn(),
  })
  store.dispatch(chatActions.clearChat())
  store.dispatch(sessionActions.setCurrentSession('image-renderer-task'))
  await i18n.changeLanguage('en')
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

const generatedArtifact = {
  id: 'image-artifact-1',
  storage: 'managed' as const,
  key: 'images/image-artifact-1.png',
  title: 'Generated football scene',
  kind: 'image' as const,
  media_type: 'image/png',
  size: 2048,
  width: 1024,
  height: 1024,
}

// Shared CI runners occasionally starve workers long enough that both the
// default 1s findByRole window and even vitest's 5s per-test ceiling expired
// while waiting for the preview button (seen on ubuntu-latest). Keep explicit
// headroom on every layer so assertions stay about behavior, not runner speed.
const SLOW_RUNNER_WAIT = { timeout: 5000 }
const SLOW_RUNNER_TEST_TIMEOUT = 20000

async function readyGeneratedImage() {
  renderImage({
    status: 'done',
    phase: 'terminal',
    outcome: 'succeeded',
    artifacts: [generatedArtifact],
  })
  const image = await screen.findByRole('img', { name: generatedArtifact.title }, SLOW_RUNNER_WAIT)
  fireEvent.load(image)
  return screen.findByRole('button', { name: 'Open image in a new window' }, SLOW_RUNNER_WAIT)
}

describe('GeneratedImageToolRenderer visibility', () => {
  it('leaves queued pre-dispatch state to the independent approval UI', () => {
    const view = renderImage()
    expect(view.container.querySelector('[data-generated-image-state]')).toBeNull()
  })

  it.each(['generating', 'saving'] as const)('shows the image placeholder once phase is %s', (phase) => {
    const view = renderImage({ phase })
    expect(view.container.querySelector(`[data-generated-image-state="${phase}"]`)).not.toBeNull()
  })

  it.each(['succeeded', 'failed'] as const)('keeps terminal %s visible', (outcome) => {
    const view = renderImage({ status: outcome === 'failed' ? 'error' : 'done', phase: 'terminal', outcome })
    expect(view.container.querySelector(`[data-generated-image-state="${outcome}"]`)).not.toBeNull()
  })

  it.each(['approval_denied', 'cancelled_before_dispatch'])(
    'hides pre-dispatch cancellation %s even when an operation ID is present',
    (errorCode) => {
      expect(shouldHideGeneratedImageCard(
        { phase: 'terminal', outcome: 'cancelled', errorCode },
      )).toBe(true)
      const view = renderImage({
        status: 'done',
        phase: 'terminal',
        outcome: 'cancelled',
        errorCode,
        operationID: 'tc-image',
      })
      expect(view.container.querySelector('[data-generated-image-state]')).toBeNull()
    },
  )

  it('keeps a post-dispatch cancelled terminal visible', () => {
    const view = renderImage({
      status: 'done',
      phase: 'terminal',
      outcome: 'cancelled',
      errorCode: 'provider_cancelled',
      operationID: 'op-dispatched',
    })
    expect(view.container.querySelector('[data-generated-image-state="cancelled"]')).not.toBeNull()
  })

  it('opens a decoded image in a separate browser window from the click stack', async () => {
    const browserOpen = vi.spyOn(window, 'open').mockReturnValue(null)
    const preview = await readyGeneratedImage()

    fireEvent.click(preview)

    expect(browserOpen).toHaveBeenCalledWith(
      'blob:generated-image', '_blank', 'noopener,noreferrer',
    )
    expect(mocks.openArtifact).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: 'Download image' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Open Artifact' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Reveal in folder' })).toBeNull()
  }, SLOW_RUNNER_TEST_TIMEOUT)

  it('opens a decoded desktop image with the hardened artifact action', async () => {
    mocks.desktop = true
    const browserOpen = vi.spyOn(window, 'open').mockReturnValue(null)
    const preview = await readyGeneratedImage()

    fireEvent.click(preview)

    await waitFor(() => expect(mocks.openArtifact).toHaveBeenCalledWith(
      'image-renderer-task', generatedArtifact.id,
    ))
    expect(browserOpen).not.toHaveBeenCalled()
  }, SLOW_RUNNER_TEST_TIMEOUT)

  it('shows a localized error when the desktop viewer cannot open the image', async () => {
    mocks.desktop = true
    mocks.openArtifact.mockRejectedValue(new Error('viewer unavailable'))
    const preview = await readyGeneratedImage()

    fireEvent.click(preview)

    expect(await screen.findByText('The image could not be opened in a new window.')).toBeTruthy()
  }, SLOW_RUNNER_TEST_TIMEOUT)
})
