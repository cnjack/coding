/**
 * SettingsView render tests (M18): the section rail, default General section,
 * tab switching to the Cloud section, and store-driven deep links.
 *
 * The view is rendered against the real singleton store + real i18n resources
 * (English). Backend calls fail in jsdom (no server), which is exactly the
 * degradation path the components already tolerate: Cloud shows the logged-out
 * login entry, General still renders its local preferences.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Provider } from 'react-redux'
import { i18n } from '../i18n'
import { store, uiActions } from '../app/store'
import { buildImageEndpointConfig, buildProviderBaseURLUpdate, SettingsView } from './SettingsView'
import { api } from '../lib/api'
import {
  removeProviderCatalogCache,
  writeProviderCatalogCache,
} from '../lib/providerCatalogCache'

function renderView() {
  return render(
    <Provider store={store}>
      <SettingsView />
    </Provider>,
  )
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

beforeEach(async () => {
  cleanup()
  vi.restoreAllMocks()
  await i18n.changeLanguage('en')
  store.dispatch(uiActions.setView('settings'))
  store.dispatch(uiActions.setSettingsTab('general'))
})

describe('SettingsView', () => {
  it('builds an independent OpenAI Images endpoint without enabling provider policy', () => {
    expect(buildImageEndpointConfig(
      true,
      ' https://images.example/v1 ',
      [{ id: ' paint-1 ', name: 'Painter', sizes: '1024x1024, 1792x1024' }],
      'cdn.example, *.assets.example',
    )).toEqual({
      protocol: 'openai_images',
      base_url: 'https://images.example/v1',
      models: [{ id: 'paint-1', name: 'Painter', sizes: ['1024x1024', '1792x1024'] }],
      asset_hosts: ['cdn.example', '*.assets.example'],
    })
    expect(buildImageEndpointConfig(false, '', [], '')).toBeUndefined()
  })

  it('uses null only when an existing provider base URL is explicitly cleared', () => {
    expect(buildProviderBaseURLUpdate('https://proxy.example/v4', '   ')).toBeNull()
    expect(buildProviderBaseURLUpdate(undefined, '')).toBeUndefined()
    expect(buildProviderBaseURLUpdate('https://proxy.example/v4', ' https://next.example/v1 '))
      .toBe('https://next.example/v1')
  })

  it('renders the section rail with every settings section', () => {
    renderView()
    // The rail is labelled navigation.
    expect(screen.getByRole('navigation', { name: 'Settings' })).toBeTruthy()
    for (const label of [
      'General',
      'Cloud',
      'Appearance',
      'Agent',
      'Review',
      'Providers',
      'MCP Servers',
      'Skills',
      'Memory',
      'Browser',
      'Computer',
      'SSH',
      'Shortcuts',
      'Usage',
      'Developer',
    ]) {
      expect(screen.getByRole('button', { name: label })).toBeTruthy()
    }
    expect(screen.queryByRole('button', { name: 'Channels' })).toBeNull()
    // Way back to the workspace.
    expect(screen.getByRole('button', { name: /Back to workspace/ })).toBeTruthy()
  })

  it('shows the General section by default', () => {
    renderView()
    // Rail marks General active.
    expect(screen.getByRole('button', { name: 'General' }).getAttribute('aria-current')).toBe('page')
    // Existing general settings items (moved here, not re-invented).
    expect(screen.getByText('Default auto-approve')).toBeTruthy()
    expect(screen.queryByText('Sync new sessions to the cloud')).toBeNull()
  })

  it('switches to the Cloud section when the rail tab is clicked', async () => {
    renderView()
    fireEvent.click(screen.getByRole('button', { name: 'Cloud' }))
    expect(store.getState().ui.settingsTab).toBe('cloud')
    expect(screen.getByRole('button', { name: 'Cloud' }).getAttribute('aria-current')).toBe('page')
    // Logged out (no backend in tests) → the device-code login entry point.
    await waitFor(() => {
      expect(screen.getByText('Log in to use this device remotely from the cloud console and mobile app.')).toBeTruthy()
      expect(screen.getByRole('button', { name: 'Log in to cloud' })).toBeTruthy()
    })
  })

  it('honours a store deep link that pre-selects the Cloud tab', async () => {
    store.dispatch(uiActions.setSettingsTab('cloud'))
    renderView()
    expect(screen.getByRole('button', { name: 'Cloud' }).getAttribute('aria-current')).toBe('page')
    await waitFor(() => expect(screen.getByRole('button', { name: 'Log in to cloud' })).toBeTruthy())
  })

  it('routes back to the chat view from the rail back action', () => {
    renderView()
    fireEvent.click(screen.getByRole('button', { name: /Back to workspace/ }))
    expect(store.getState().ui.activeView).toBe('chat')
  })

  it('renders manifest-backed provider web search and saves its independent policy', async () => {
    vi.spyOn(api, 'listProviders').mockResolvedValue([{
      id: 'zhipuai-coding-plan',
      api_key_set: true,
      provider_tools: {},
      capabilities: [{
        id: 'web_search', availability: 'supported', mechanism: 'mcp_tool',
        model_label: 'web_search_prime', enabled: false,
        max_calls_per_turn: 2, max_calls_per_session: 10,
      }],
    }])
    vi.spyOn(api, 'providerCatalog').mockResolvedValue([])
    vi.spyOn(api, 'setupProviders').mockResolvedValue([])
    vi.spyOn(api, 'models').mockResolvedValue({
      current: { provider: '', model: '' }, current_image: { provider: '', model: '' }, providers: [],
    })
    const update = vi.spyOn(api, 'updateProvider').mockResolvedValue({ status: 'ok' })

    store.dispatch(uiActions.setSettingsTab('providers'))
    renderView()
    const toggle = await screen.findByRole('switch', { name: 'Provider web search' })
    expect(screen.getByText(/mcp_tool · web_search_prime/)).toBeTruthy()
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    fireEvent.click(toggle)
    await waitFor(() => expect(update).toHaveBeenCalledWith(
      'zhipuai-coding-plan',
      { provider_tools: { web_search: { enabled: true } } },
    ))
    expect(toggle.getAttribute('aria-checked')).toBe('true')
  })

  it('renders a cached provider catalog before background revalidation completes', async () => {
    const provider = {
      id: 'cached-copilot', name: 'Cached Copilot', api_key_set: false,
      auth_binding: { method: 'github_copilot' as const, account_id: 'account-cache' },
      auth_status: {
        method: 'github_copilot' as const,
        default_account_id: 'account-cache',
        accounts: [{
          id: 'account-cache', login: 'cached-user', authenticated_at: '2026-08-09T08:00:00Z', requires_reauth: false,
        }],
      },
      capabilities: [],
    }
    removeProviderCatalogCache(provider.id)
    writeProviderCatalogCache(provider, [{ id: 'cached-model', name: 'Cached Model', added: true }])
    const liveCatalog = deferred<Array<{ id: string; name: string; added: boolean }>>()
    vi.spyOn(api, 'listProviders').mockResolvedValue([provider])
    vi.spyOn(api, 'providerCatalog').mockReturnValue(liveCatalog.promise)
    vi.spyOn(api, 'setupProviders').mockResolvedValue([])
    vi.spyOn(api, 'models').mockResolvedValue({
      current: { provider: '', model: '' }, current_image: { provider: '', model: '' }, providers: [],
    })

    store.dispatch(uiActions.setSettingsTab('providers'))
    renderView()
    expect(await screen.findByText('Cached Model')).toBeTruthy()
    expect(api.providerCatalog).toHaveBeenCalledWith(provider.id)

    await act(async () => {
      liveCatalog.resolve([{ id: 'fresh-model', name: 'Fresh Model', added: true }])
      await Promise.resolve()
    })
    expect(await screen.findByText('Fresh Model')).toBeTruthy()
    expect(screen.queryByText('Cached Model')).toBeNull()
    removeProviderCatalogCache(provider.id)
  })

  it('does not let an older catalog request overwrite a newer refresh', async () => {
    const provider = {
      id: 'racing-copilot', name: 'Racing Copilot', api_key_set: false,
      auth_binding: { method: 'github_copilot' as const, account_id: 'account-race' },
      capabilities: [],
    }
    removeProviderCatalogCache(provider.id)
    const initialRequest = deferred<Array<{ id: string; name: string; added: boolean }>>()
    const refreshRequest = deferred<Array<{ id: string; name: string; added: boolean }>>()
    vi.spyOn(api, 'listProviders').mockResolvedValue([provider])
    vi.spyOn(api, 'providerCatalog')
      .mockReturnValueOnce(initialRequest.promise)
      .mockReturnValueOnce(refreshRequest.promise)
    vi.spyOn(api, 'setupProviders').mockResolvedValue([])
    vi.spyOn(api, 'models').mockResolvedValue({
      current: { provider: '', model: '' }, current_image: { provider: '', model: '' }, providers: [],
    })

    store.dispatch(uiActions.setSettingsTab('providers'))
    renderView()
    await screen.findByText('Racing Copilot')
    fireEvent.click(screen.getByTitle('Refresh catalog'))
    expect(api.providerCatalog).toHaveBeenCalledTimes(2)

    await act(async () => {
      refreshRequest.resolve([{ id: 'new-model', name: 'New Model', added: true }])
      await Promise.resolve()
    })
    expect(await screen.findByText('New Model')).toBeTruthy()

    await act(async () => {
      initialRequest.resolve([{ id: 'old-model', name: 'Old Model', added: true }])
      await Promise.resolve()
    })
    expect(screen.queryByText('Old Model')).toBeNull()
    expect(screen.getByText('New Model')).toBeTruthy()
    removeProviderCatalogCache(provider.id)
  })

  it('sends base_url null when Settings clears a BigModel proxy without resending the API key', async () => {
    vi.spyOn(api, 'listProviders').mockResolvedValue([{
      id: 'zhipuai-coding-plan', name: 'BigModel Coding Plan', api_key_set: true,
      base_url: 'https://proxy.example.test/v4', provider_tools: {}, capabilities: [],
    }])
    vi.spyOn(api, 'providerCatalog').mockResolvedValue([])
    vi.spyOn(api, 'setupProviders').mockResolvedValue([])
    vi.spyOn(api, 'models').mockResolvedValue({
      current: { provider: 'zhipuai-coding-plan', model: 'glm-4.7' },
      current_image: { provider: '', model: '' },
      providers: [],
    })
    const update = vi.spyOn(api, 'updateProvider').mockResolvedValue({ status: 'ok' })

    store.dispatch(uiActions.setSettingsTab('providers'))
    renderView()
    await screen.findByText('BigModel Coding Plan')
    fireEvent.click(screen.getByTitle('Edit provider'))
    const endpoint = await screen.findByDisplayValue('https://proxy.example.test/v4')
    fireEvent.change(endpoint, { target: { value: '' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(update).toHaveBeenCalledTimes(1))
    const [providerID, request] = update.mock.calls[0]
    expect(providerID).toBe('zhipuai-coding-plan')
    expect(request.base_url).toBeNull()
    expect(request.api_key).toBeUndefined()
  })

  it('makes the selected image model directly available without extra enable controls', async () => {
    vi.spyOn(api, 'listProviders').mockResolvedValue([{
      id: 'image-provider', name: 'Image Provider', api_key_set: true,
      provider_tools: { image_generation: { enabled: false } },
      capabilities: [{
        id: 'image_generation', availability: 'supported', mechanism: 'openai_images',
        model_label: 'paint-1', enabled: false, max_calls_per_turn: 1, max_calls_per_session: 20,
      }],
    }])
    vi.spyOn(api, 'providerCatalog').mockResolvedValue([])
    vi.spyOn(api, 'setupProviders').mockResolvedValue([])
    vi.spyOn(api, 'models').mockResolvedValue({
      current: { provider: '', model: '' },
      current_image: { provider: 'image-provider', model: 'paint-1' },
      providers: [{
        id: 'image-provider', name: 'Image Provider', kind: 'image-provider', source: 'desktop',
        models: [{
          id: 'paint-1', name: 'Painter', tool_call: false, enabled: true,
          input_modalities: ['text'], output_modalities: ['image'],
          capability_availability: 'supported',
        }],
      }],
    })
    store.dispatch(uiActions.setSettingsTab('providers'))
    renderView()
    await screen.findByText('1 image candidates · 1 integrated')
    expect(screen.getByText('Independent from the chat model and its provider. Select an available Image Model to let the Agent generate images. Calls may incur provider charges; Full access does not ask each time.')).toBeTruthy()
    expect(screen.queryByText('Separate Image Model')).toBeNull()
    expect(screen.queryByText('Provider tool')).toBeNull()
    expect(screen.queryByText('Current task')).toBeNull()
    expect(screen.queryByRole('switch', { name: 'Image generation tool' })).toBeNull()
  })

  it('adds an OpenAI provider with a managed ChatGPT account and no API key', async () => {
    vi.spyOn(api, 'listProviders').mockResolvedValue([])
    vi.spyOn(api, 'providerCatalog').mockResolvedValue([])
    vi.spyOn(api, 'setupProviders').mockResolvedValue([{
      id: 'openai', name: 'OpenAI', configured: false, auth_methods: ['api_key', 'codex_oauth'],
    }])
    vi.spyOn(api, 'providerAuthStatus').mockResolvedValue({
      method: 'codex_oauth',
      default_account_id: 'account-1',
      accounts: [{
        id: 'account-1', login: 'jack@example.com', authenticated_at: '2026-08-09T08:00:00Z', requires_reauth: false,
      }],
    })
    vi.spyOn(api, 'models').mockResolvedValue({
      current: { provider: '', model: '' }, current_image: { provider: '', model: '' }, providers: [],
    })
    const addResponse = deferred<{ status: string }>()
    const add = vi.spyOn(api, 'addProvider').mockReturnValue(addResponse.promise)

    store.dispatch(uiActions.setSettingsTab('providers'))
    renderView()
    fireEvent.click(await screen.findByRole('button', { name: 'Add' }))
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'openai' } })
    fireEvent.click(await screen.findByRole('button', { name: 'ChatGPT' }))

    await screen.findByText('jack@example.com')
    expect(screen.queryByLabelText('API Key')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Advanced' }))
    expect(screen.queryByText('Custom Endpoint')).toBeNull()
    expect(screen.queryByText('Custom Headers')).toBeNull()
    const reasoningSelect = screen.getByText('Reasoning effort').parentElement?.querySelector('select')
    expect(reasoningSelect).toBeTruthy()
    fireEvent.change(reasoningSelect!, { target: { value: 'high' } })
    expect(screen.queryByText('OpenAI-compatible image endpoint')).toBeNull()
    const save = screen.getByRole('button', { name: 'Add' })
    await waitFor(() => expect(save.hasAttribute('disabled')).toBe(false))
    fireEvent.click(save)

    await waitFor(() => expect(add).toHaveBeenCalledTimes(1))
    expect(screen.getByRole('group', { name: 'Authentication' }).hasAttribute('disabled')).toBe(true)
    expect(add.mock.calls[0][0]).toMatchObject({
      id: 'openai',
      auth_binding: { method: 'codex_oauth' },
      reasoning_effort: 'high',
    })
    expect(add.mock.calls[0][0].api_key).toBeUndefined()
    expect(add.mock.calls[0][0]).not.toHaveProperty('base_url')
    expect(add.mock.calls[0][0]).not.toHaveProperty('headers')
    expect(add.mock.calls[0][0]).not.toHaveProperty('image_endpoint')
    await act(async () => {
      addResponse.resolve({ status: 'ok' })
      await Promise.resolve()
    })
  })

  it('clears an existing image endpoint when a provider uses managed authentication', async () => {
    const connected = {
      method: 'codex_oauth' as const,
      default_account_id: 'account-1',
      accounts: [{
        id: 'account-1', login: 'jack@example.com', authenticated_at: '2026-08-09T08:00:00Z', requires_reauth: false,
      }],
    }
    vi.spyOn(api, 'listProviders').mockResolvedValue([{
      id: 'openai', name: 'OpenAI', api_key_set: false,
      auth_methods: ['api_key', 'codex_oauth'],
      auth_binding: { method: 'codex_oauth', account_id: 'account-1' },
      auth_status: connected,
      image_endpoint: {
        protocol: 'openai_images', base_url: 'https://images.example/v1', models: [{ id: 'paint-1' }],
      },
      capabilities: [],
    }])
    vi.spyOn(api, 'providerCatalog').mockResolvedValue([])
    vi.spyOn(api, 'setupProviders').mockResolvedValue([{
      id: 'openai', name: 'OpenAI', configured: true, auth_methods: ['api_key', 'codex_oauth'],
    }])
    vi.spyOn(api, 'providerAuthStatus').mockResolvedValue(connected)
    vi.spyOn(api, 'models').mockResolvedValue({
      current: { provider: '', model: '' }, current_image: { provider: '', model: '' }, providers: [],
    })
    const update = vi.spyOn(api, 'updateProvider').mockResolvedValue({ status: 'ok' })

    store.dispatch(uiActions.setSettingsTab('providers'))
    renderView()
    fireEvent.click(await screen.findByTitle('Edit provider'))
    await screen.findByText('jack@example.com')
    expect(screen.queryByText('OpenAI-compatible image endpoint')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(update).toHaveBeenCalledTimes(1))
    const request = update.mock.calls[0][1]
    expect(request.image_endpoint).toBeNull()
    expect(request).not.toHaveProperty('base_url')
    expect(request).not.toHaveProperty('headers')
  })

  it('surfaces a provider account that needs re-authentication', async () => {
    vi.spyOn(api, 'listProviders').mockResolvedValue([{
      id: 'openai', name: 'OpenAI', api_key_set: false,
      auth_binding: { method: 'codex_oauth', account_id: 'account-1' },
      auth_status: {
        method: 'codex_oauth', default_account_id: 'account-1', accounts: [{
          id: 'account-1', login: 'jack@example.com', authenticated_at: '2026-08-09T08:00:00Z', requires_reauth: true,
        }],
      },
      capabilities: [],
    }])
    vi.spyOn(api, 'providerCatalog').mockResolvedValue([])
    vi.spyOn(api, 'setupProviders').mockResolvedValue([])
    vi.spyOn(api, 'models').mockResolvedValue({
      current: { provider: '', model: '' }, current_image: { provider: '', model: '' }, providers: [],
    })

    store.dispatch(uiActions.setSettingsTab('providers'))
    renderView()

    expect(await screen.findByText(/Needs re-authentication · jack@example.com · ChatGPT/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Re-authenticate' })).toBeTruthy()
  })
})
