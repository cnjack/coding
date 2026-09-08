/**
 * SettingsView — the standalone settings page (M18; formerly the SettingsDialog
 * full-screen overlay, itself a React port of web/src/components/SettingsDialog.vue).
 *
 * A first-class app view (`ui.activeView === 'settings'`) rendered inside the
 * workspace main column: its own left nav rail + an inset surface content
 * panel — the same geometry as the chat page, NOT a small centered dialog.
 * Tabs: General (server/token/auto-approve/language + the M19 cloud-sync
 * default), Cloud (M18: account/connection state, auto-connect, pairing
 * approvals, device-code login and logout — moved out of the
 * CloudBadge popover), Agent (tool discovery), Review (Auto approval review),
 * Providers (full CRUD + catalog + advanced config),
 * Models (state/favorites/effort), MCP (servers CRUD + OAuth login), Skills
 * (enable/disable), Appearance (theme picker), Memory (status + consolidation),
 * Browser (config + site permissions), Computer (config + app permissions +
 * grants), Remote (SSH aliases), Usage (stats).
 *
 * The active tab lives in the redux store (`ui.settingsTab`) so other surfaces
 * can deep-link — e.g. the CloudBadge popover's "Open settings" routes here
 * with the Cloud tab active.
 *
 * State is per-tab (each tab remounts on activation, so switching tabs naturally
 * abandons in-progress sub-flows — mirroring the Vue `watch(activeTab)` reset).
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Cog6ToothIcon,
  ArrowLeftIcon,
  PlusIcon,
  TrashIcon,
  ChevronDownIcon,
  PencilSquareIcon,
  CpuChipIcon,
  ServerStackIcon,
  SparklesIcon,
  GlobeAltIcon,
  CommandLineIcon,
  SwatchIcon,
  ChartBarIcon,
  CheckIcon,
  ServerIcon,
  BoltIcon,
  ComputerDesktopIcon,
  ShieldCheckIcon,
  KeyIcon,
  LockClosedIcon,
  XMarkIcon,
  MinusIcon,
  ArrowPathIcon,
  ArrowTopRightOnSquareIcon,
  ExclamationTriangleIcon,
  CircleStackIcon,
  WrenchScrewdriverIcon,
  CloudIcon,
  PhotoIcon,
} from '@heroicons/react/24/outline'
import { useTranslation } from 'react-i18next'
import { useAppDispatch, useAppSelector } from '../app/hooks'
import { uiActions, modelActions, loadConfig, loadModels, type SettingsTab } from '../app/store'
import { ProviderIcon } from './ProviderIcon'
import { CloudTab } from './settings/CloudTab'
import {
  ProviderAuthSection,
  isProviderAuthReady,
  providerCredentialMethods,
  resolveProviderAuthAccount,
} from './settings/ProviderAuthSection'
import {
  BTN_DANGER,
  BTN_GHOST,
  BTN_PRIMARY,
  BTN_SECONDARY,
  BTN_SM,
  BTN_XS,
  CHIP,
  CHIP_ACCENT,
  EmptyState,
  Field,
  INPUT,
  INPUT_MONO,
  INPUT_SM,
  LABEL,
  ROW,
  SECTION_TITLE,
  Segmented,
  Switch,
  TEXTAREA,
} from './settings/atoms'
import { api } from '../lib/api'
import {
  readProviderCatalogCache,
  removeProviderCatalogCache,
  writeProviderCatalogCache,
} from '../lib/providerCatalogCache'
import { openRemoteConnect } from '../lib/remote'
import { isTauri, openUrl } from '../lib/useDesktop'
import { useAppUpdate } from '../lib/useAppUpdate'
import { LOCALE_LABELS, SUPPORTED_LOCALES, setLocale, type SupportedLocale } from '../i18n'
import type {
  BrowserConfig,
  BrowserStatusResponse,
  BrowserSitePermission,
  ComputerConfig,
  ComputerStatusResponse,
  ComputerAppPermission,
  ComputerPermissionState,
  ToolSearchStatusResponse,
  MemoryConfig,
  MemoryStatusResponse,
  DevOptionsStatusResponse,
} from '../lib/api'
import type { ApprovalReviewConfig, ApprovalReviewDefaults } from '../lib/types'
import type {
  ProviderDetail,
  ImageEndpointConfig,
  CustomModelDetail,
  CatalogModel,
  MCPServerInfo,
  MCPServerRequest,
  SkillInfo,
  SSHListResponse,
  UsageStats,
  SetupProvider,
  ProviderAuthBinding,
  ProviderAuthStatus,
  ProviderCredentialMethod,
} from '../lib/types'

// ─── tab config ────────────────────────────────────────────────────────────

// Models live inside Providers (catalog + custom models); Memory is a React
// settings surface backed by the project-scoped memory APIs. The tab id type
// (SettingsTab) lives in the store so other surfaces can deep-link to a tab.
type TabId = SettingsTab

const TABS: { id: TabId; Icon: React.ComponentType<{ className?: string }> }[] = [
  { id: 'general', Icon: Cog6ToothIcon },
  { id: 'cloud', Icon: CloudIcon },
  { id: 'appearance', Icon: SwatchIcon },
  { id: 'agent', Icon: BoltIcon },
  { id: 'review', Icon: ShieldCheckIcon },
  { id: 'providers', Icon: CpuChipIcon },
  { id: 'mcp', Icon: ServerStackIcon },
  { id: 'skills', Icon: SparklesIcon },
  { id: 'memory', Icon: CircleStackIcon },
  { id: 'browser', Icon: GlobeAltIcon },
  { id: 'computer', Icon: ComputerDesktopIcon },
  { id: 'ssh', Icon: CommandLineIcon },
  { id: 'shortcuts', Icon: KeyIcon },
  { id: 'usage', Icon: ChartBarIcon },
  { id: 'developer', Icon: WrenchScrewdriverIcon },
]

const THEMES: { id: string; label: string; appearance: 'dark' | 'light' }[] = [
  { id: 'jcode-dark', label: 'jcode Dark', appearance: 'dark' },
  { id: 'jcode-light', label: 'jcode Light', appearance: 'light' },
  { id: 'midnight', label: 'Midnight', appearance: 'dark' },
  { id: 'dracula', label: 'Dracula', appearance: 'dark' },
  { id: 'nord-dark', label: 'Nord Dark', appearance: 'dark' },
  { id: 'github-light', label: 'GitHub Light', appearance: 'light' },
  { id: 'solarized-light', label: 'Solarized Light', appearance: 'light' },
]

// ─── helpers ────────────────────────────────────────────────────────────────

/** Compact context-window size: <1000 raw, >=1000 as K (200000 → "200K"). */
function formatContext(ctx?: number): string {
  if (!ctx || ctx <= 0) return ''
  if (ctx < 1000) return String(ctx)
  return Math.round(ctx / 1000) + 'K'
}

function parseTiers(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

function buildHeaders(rows: { key: string; value: string }[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const r of rows) {
    const k = r.key.trim()
    if (k) out[k] = r.value
  }
  return out
}

function cmToDetail(cm: CatalogModel): CustomModelDetail {
  return {
    id: cm.id,
    name: cm.name,
    reasoning: cm.reasoning,
    context: cm.context,
    attachment: cm.attachment,
    effort_tiers: cm.effort_tiers ? [...cm.effort_tiers] : [],
    custom: true,
  }
}

function mcpStatusLabel(info: MCPServerInfo, t: (k: string) => string): string {
  if (!info.enabled) return t('settings.mcp.status.disabled')
  switch (info.status) {
    case 'connected':
      return t('settings.mcp.status.connected')
    case 'needs_auth':
      return t('settings.mcp.status.loginRequired')
    case 'error':
      return t('settings.mcp.status.error')
    default:
      return t('settings.mcp.status.configured')
  }
}

function mcpStatusColor(info: MCPServerInfo): string {
  if (!info.enabled) return 'var(--color-muted-foreground)'
  switch (info.status) {
    case 'connected':
      return 'var(--color-success-fg)'
    case 'needs_auth':
      return 'var(--color-warning-fg)'
    case 'error':
      return 'var(--color-error-fg)'
    default:
      return 'var(--color-muted-foreground)'
  }
}

// ─── main view ──────────────────────────────────────────────────────────────

export function SettingsView() {
  const dispatch = useAppDispatch()
  const { t } = useTranslation()
  // The active tab lives in the store so other surfaces (CloudBadge, command
  // palette) can deep-link to a specific section.
  const tab = useAppSelector((s) => s.ui.settingsTab)
  const setTab = (id: TabId) => dispatch(uiActions.setSettingsTab(id))

  const back = () => dispatch(uiActions.setView('chat'))

  return (
    // First-class view inside the workspace main column (M18): the global
    // Sidebar stays visible; this surface brings its own section rail + inset
    // content panel, same geometry as the chat page.
    <div className="settings-shell flex min-h-0 flex-1 overflow-hidden bg-[var(--color-background)] text-[var(--color-foreground)]">
      {/* Section rail: shell tone, no border. Holds the vertical section nav
          (full-width buttons, active one highlighted) with a "Back" action
          pinned to the bottom. */}
      <nav
        aria-label={t('nav.settings')}
        className="flex w-52 shrink-0 flex-col gap-0.5 overflow-y-auto p-3"
        style={{ backgroundColor: 'var(--color-background)' }}
      >
        {TABS.map((tabItem) => {
          const active = tab === tabItem.id
          return (
            <button
              key={tabItem.id}
              type="button"
              aria-current={active ? 'page' : undefined}
              onClick={() => setTab(tabItem.id)}
              className="relative flex h-8 w-full items-center gap-2.5 rounded-[var(--radius-md)] px-2.5 text-left text-[13px] transition-colors duration-[var(--duration-fast,150ms)] hover:bg-[var(--color-secondary)]"
              style={
                active
                  ? { color: 'var(--color-foreground)', backgroundColor: 'var(--color-secondary)', fontWeight: 500 }
                  : { color: 'var(--color-muted-foreground)', backgroundColor: 'transparent' }
              }
            >
              {active && (
                <span
                  className="absolute left-0 top-1/2 h-4 w-[3px] -translate-y-1/2 rounded-full"
                  style={{ backgroundColor: 'var(--color-accent-neutral)' }}
                />
              )}
              <tabItem.Icon className="h-3.5 w-3.5 shrink-0" />
              <span className="truncate">{t(`settings.tabs.${tabItem.id}`)}</span>
            </button>
          )
        })}

        {/* Back action pinned to the bottom of the rail — returning shouldn't
            require traveling all the way back to the top. */}
        <button
          type="button"
          onClick={back}
          title={`${t('settings.backToWorkspace')} (Esc)`}
          className="group mt-auto flex h-9 items-center gap-1.5 rounded-[var(--radius-md)] px-2.5 text-[13px] font-medium text-[var(--color-muted-foreground)] transition-colors hover:bg-[var(--color-secondary)] hover:text-[var(--color-foreground)]"
        >
          <ArrowLeftIcon className="h-4 w-4 transition-transform group-hover:-translate-x-0.5" />
          {t('settings.backToWorkspace')}
        </button>
      </nav>

      {/* Right column: inset surface content panel (mirrors .settings-panel /
          .chat-panel). Only this panel scrolls; each section is centered and
          width-capped (max-w-3xl). No header bar — the rail conveys navigation. */}
      <div className="flex min-w-0 flex-1 flex-col">
        <div
          className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-[var(--radius-2xl)] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-sm)]"
          style={{ margin: '4px 14px 14px 4px' }}
        >
          <div className="min-h-0 flex-1 overflow-y-auto px-8 py-7 [&>*]:mx-auto [&>*]:max-w-3xl">
            {tab === 'general' && <GeneralTab />}
            {tab === 'cloud' && <CloudTab />}
            {tab === 'appearance' && <AppearanceTab />}
            {tab === 'agent' && <AgentTab />}
            {tab === 'review' && <ReviewTab />}
            {tab === 'providers' && <ProvidersTab />}
            {tab === 'mcp' && <MCPTab />}
            {tab === 'skills' && <SkillsTab />}
            {tab === 'memory' && <MemoryTab />}
            {tab === 'browser' && <BrowserTab />}
            {tab === 'computer' && <ComputerTab />}
            {tab === 'ssh' && <SSHTab />}
            {tab === 'shortcuts' && <ShortcutsTab />}
            {tab === 'usage' && <UsageTab />}
            {tab === 'developer' && <DeveloperTab />}
          </div>
        </div>
      </div>
    </div>
  )
}

// ════════════════════════════════════════════════════════════════════════════
// Providers tab — full port: list + add/edit form + catalog + advanced config
// ════════════════════════════════════════════════════════════════════════════

function ProvidersTab() {
  const { t } = useTranslation()
  const dispatch = useAppDispatch()
  const activeProvider = useAppSelector((s) => s.model.providerName)
  const activeModel = useAppSelector((s) => s.model.modelName)

  const [providers, setProviders] = useState<ProviderDetail[]>([])
  const [loading, setLoading] = useState(true)
  const [setupList, setSetupList] = useState<SetupProvider[]>([])
  const [catalogs, setCatalogs] = useState<Record<string, CatalogModel[]>>({})
  const [catalogLoading, setCatalogLoading] = useState('')
  const catalogRequestEpochs = useRef(new Map<string, number>())
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<ProviderDetail | null>(null)
  const [modelForm, setModelForm] = useState<{ providerId: string; target: CustomModelDetail | null } | null>(null)

  // Model roles: config.small_model ("provider/model", '' = unset). Options
  // come from the chat-picker payload in redux (enabled models only).
  const smallModel = useAppSelector((s) => s.model.smallModel)
  const imageModel = useAppSelector((s) => s.model.imageModel)
  const pickerProviders = useAppSelector((s) => s.model.providers)
  const currentTaskID = useAppSelector((s) => s.session.currentSessionId)
  const [smallSaving, setSmallSaving] = useState(false)
  const [smallError, setSmallError] = useState('')
  const [smallSaved, setSmallSaved] = useState(false)
  const [imageSaving, setImageSaving] = useState(false)
  const [imageError, setImageError] = useState('')
  const [imageSaved, setImageSaved] = useState(false)
  const [policySaving, setPolicySaving] = useState<Set<string>>(() => new Set())
  const [policyErrors, setPolicyErrors] = useState<Record<string, string>>({})

  // Refresh the chat model picker after provider/model mutations.
  async function refreshModels() {
    try {
      const resp = await api.models(currentTaskID || undefined)
      dispatch(modelActions.setProviders(resp.providers))
      dispatch(modelActions.setImageModel(
        resp.current_image?.provider && resp.current_image?.model
          ? `${resp.current_image.provider}/${resp.current_image.model}`
          : '',
      ))
    } catch {
      /* ignore */
    }
  }

  function beginCatalogRequest(providerId: string): number {
    const epoch = (catalogRequestEpochs.current.get(providerId) ?? 0) + 1
    catalogRequestEpochs.current.set(providerId, epoch)
    return epoch
  }

  function catalogRequestIsCurrent(providerId: string, epoch: number): boolean {
    return catalogRequestEpochs.current.get(providerId) === epoch
  }

  async function revalidateCatalog(provider: ProviderDetail, showLoading = false): Promise<void> {
    const epoch = beginCatalogRequest(provider.id)
    if (showLoading) setCatalogLoading(provider.id)
    try {
      const catalog = await api.providerCatalog(provider.id)
      if (!catalogRequestIsCurrent(provider.id, epoch)) return
      writeProviderCatalogCache(provider, catalog)
      setCatalogs((current) => ({ ...current, [provider.id]: catalog }))
    } catch {
      // Keep the last successful cache. A transient provider outage must not
      // make known models disappear when reopening Settings.
    } finally {
      if (showLoading && catalogRequestIsCurrent(provider.id, epoch)) {
        setCatalogLoading('')
      }
    }
  }

  async function load() {
    setLoading(true)
    try {
      const list = await api.listProviders()
      setProviders(list)
      setCatalogs(Object.fromEntries(list.map((provider) => [
        provider.id,
        readProviderCatalogCache(provider) ?? [],
      ])))
      // Stale-while-revalidate: cards render the last successful account-aware
      // catalog immediately, then replace it only after the live request wins.
      setLoading(false)
      void Promise.all(list.map((provider) => revalidateCatalog(provider)))
    } catch {
      /* ignore */
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // Fetch the registry list for the add-provider picker.
    api.setupProviders().then(setSetupList).catch(() => {})
    // Fresh enabled-model list + small_model value for the roles section (the
    // dialog can open long after boot; another client may have changed both).
    void refreshModels()
    void dispatch(loadConfig())
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Set or clear config.small_model. The select is controlled by redux state,
  // so a failed save simply never moves it — no manual revert needed.
  async function changeSmallModel(value: string) {
    setSmallError('')
    setSmallSaved(false)
    let provider = ''
    let model = ''
    if (value) {
      // Provider ids never contain '/'; model ids may (custom endpoints).
      const i = value.indexOf('/')
      if (i >= 0) {
        provider = value.slice(0, i)
        model = value.slice(i + 1)
      } else {
        // Malformed ref without a separator (hand-edited config surfaced via
        // the "unavailable" option): pass it as model-only so the API rejects
        // it intact instead of us mangling the provider name.
        model = value
      }
    }
    setSmallSaving(true)
    try {
      await api.setSmallModel(provider, model)
      dispatch(modelActions.setSmallModel(value))
      setSmallSaved(true)
      window.setTimeout(() => setSmallSaved(false), 2000)
    } catch (err) {
      setSmallError(err instanceof Error ? err.message : String(err))
    } finally {
      setSmallSaving(false)
    }
  }

  async function changeImageModel(value: string) {
    setImageError('')
    setImageSaved(false)
    const separator = value.indexOf('/')
    const provider = separator >= 0 ? value.slice(0, separator) : ''
    const model = separator >= 0 ? value.slice(separator + 1) : value
    setImageSaving(true)
    try {
      await api.setImageModel(provider, model)
      dispatch(modelActions.setImageModel(value))
      setImageSaved(true)
      window.setTimeout(() => setImageSaved(false), 2000)
    } catch (err) {
      setImageError(err instanceof Error ? err.message : String(err))
    } finally {
      setImageSaving(false)
    }
  }

  async function toggleProviderPolicy(provider: ProviderDetail, capabilityID: 'web_search') {
    const capability = provider.capabilities?.find((candidate) => candidate.id === capabilityID)
    if (!capability) return
    const currentTools = provider.provider_tools ?? {}
    const current = currentTools[capabilityID] ?? {}
    if (!current.enabled && capability.availability !== 'supported') return
    const nextTools = {
      ...currentTools,
      [capabilityID]: { ...current, enabled: !current.enabled },
    }
    setPolicyErrors((errors) => ({ ...errors, [provider.id]: '' }))
    setPolicySaving((ids) => new Set(ids).add(provider.id))
    setProviders((list) => list.map((item) => item.id === provider.id ? {
      ...item,
      provider_tools: nextTools,
      capabilities: (item.capabilities ?? []).map((candidate) => candidate.id === capabilityID
        ? { ...candidate, enabled: !current.enabled }
        : candidate),
    } : item))
    try {
      await api.updateProvider(provider.id, { provider_tools: nextTools })
    } catch (err) {
      setProviders((list) => list.map((item) => item.id === provider.id ? provider : item))
      setPolicyErrors((errors) => ({ ...errors, [provider.id]: err instanceof Error ? err.message : String(err) }))
    } finally {
      setPolicySaving((ids) => {
        const next = new Set(ids)
        next.delete(provider.id)
        return next
      })
    }
  }

  async function refreshCatalog(providerId: string) {
    const provider = providers.find((candidate) => candidate.id === providerId)
    if (provider) await revalidateCatalog(provider, true)
  }

  async function onProviderSaved() {
    const nextProviders = await api.listProviders()
    setProviders(nextProviders)
    setCatalogs(Object.fromEntries(nextProviders.map((provider) => [
      provider.id,
      readProviderCatalogCache(provider) ?? [],
    ])))
    await Promise.all(nextProviders.map((provider) => revalidateCatalog(provider)))
    await refreshModels()
    setEditing(null)
    setAdding(false)
  }

  async function onProviderAuthenticated(providerId: string) {
    let refreshedProvider: ProviderDetail | undefined
    try {
      const nextProviders = await api.listProviders()
      refreshedProvider = nextProviders.find((provider) => provider.id === providerId)
      setProviders(nextProviders)
    } catch {
      // The account is already stored; keep the form usable if an older server
      // cannot yet project managed-auth state onto provider details.
    }
    if (refreshedProvider) {
      const cached = readProviderCatalogCache(refreshedProvider)
      setCatalogs((current) => ({ ...current, [providerId]: cached ?? [] }))
      await revalidateCatalog(refreshedProvider, true)
    }
    await refreshModels()
  }

  async function deleteProvider(id: string) {
    try {
      await api.deleteProvider(id)
      beginCatalogRequest(id)
      removeProviderCatalogCache(id)
      setProviders((prev) => prev.filter((p) => p.id !== id))
      await refreshModels()
    } catch (err) {
      console.error('Failed to delete provider:', err)
    }
  }

  // Toggle a catalog row's enabled state (registry models live in model_state;
  // toggling adds/removes them from the chat picker). Optimistic, revert on
  // failure.
  async function toggleCatalogModel(providerId: string, modelId: string) {
    const list = catalogs[providerId]
    if (!list) return
    const m = list.find((x) => x.id === modelId)
    if (!m) return
    const next = !m.added
    setCatalogs((prev) => ({
      ...prev,
      [providerId]: prev[providerId].map((x) => (x.id === modelId ? { ...x, added: next } : x)),
    }))
    try {
      await api.toggleModelEnabled(providerId, modelId, next)
      await refreshModels()
    } catch (err) {
      setCatalogs((prev) => ({
        ...prev,
        [providerId]: prev[providerId].map((x) => (x.id === modelId ? { ...x, added: !next } : x)),
      }))
      console.error('Failed to toggle model:', err)
    }
  }

  // Add/edit a custom model: rebuild the provider's custom_models (only
  // user-defined models are persisted) and save via updateProvider.
  async function saveCustomModel(payload: {
    providerId: string
    id: string
    name?: string
    reasoning: boolean
    context: number
    attachment: boolean
    effortTiers: string[]
    isEdit: boolean
    originalId?: string
  }) {
    const p = providers.find((x) => x.id === payload.providerId)
    if (!p) return
    const newId = payload.id.trim()
    const isSelf = payload.isEdit && payload.originalId === newId
    // Reject duplicate ids (collisions with built-in registry models included).
    if (!isSelf) {
      const clash = (p.custom_models ?? []).some((m) => m.id === newId)
      if (clash) {
        // Surface via alert-style: the inline form reads its own error from
        // the parent through a callback would be cleaner; for now throw to be
        // caught by the form's try/catch.
        throw new Error('A model with this ID already exists for this provider.')
      }
    }
    const next: CustomModelDetail[] = (p.custom_models ?? [])
      .filter((m) => m.custom)
      .map((m) => ({
        id: m.id,
        name: m.name,
        reasoning: m.reasoning,
        context: m.context,
        attachment: m.attachment,
        effort_tiers: m.effort_tiers,
      }))
    const built: CustomModelDetail = {
      id: newId,
      name: payload.name,
      reasoning: payload.reasoning,
      context: payload.context || undefined,
      attachment: payload.attachment || undefined,
      effort_tiers: payload.effortTiers.length ? payload.effortTiers : undefined,
    }
    if (payload.isEdit && payload.originalId) {
      const i = next.findIndex((m) => m.id === payload.originalId)
      if (i >= 0) next[i] = built
      else next.push(built)
    } else {
      next.push(built)
    }
    await api.updateProvider(payload.providerId, { custom_models: next })
    setProviders(await api.listProviders())
    await refreshCatalog(payload.providerId)
    await refreshModels()
    setModelForm(null)
  }

  async function removeCustomModel(providerId: string, modelId: string) {
    const p = providers.find((x) => x.id === providerId)
    if (!p) return
    const next: CustomModelDetail[] = (p.custom_models ?? [])
      .filter((m) => m.custom && m.id !== modelId)
      .map((m) => ({
        id: m.id,
        name: m.name,
        reasoning: m.reasoning,
        context: m.context,
        attachment: m.attachment,
        effort_tiers: m.effort_tiers,
      }))
    try {
      await api.updateProvider(providerId, { custom_models: next })
      setProviders(await api.listProviders())
      await refreshCatalog(providerId)
      await refreshModels()
    } catch (err) {
      console.error('Failed to remove model:', err)
    }
  }

  // Add form overrides the list; edit form renders in place of the list too.
  if (adding || editing) {
    return (
      <ProviderForm
        key={editing?.id ?? 'new'}
        editing={editing}
        setupList={setupList}
        configuredIds={providers.map((p) => p.id)}
        onCancel={() => {
          setAdding(false)
          setEditing(null)
        }}
        onSaved={onProviderSaved}
        onAuthenticated={onProviderAuthenticated}
      />
    )
  }

  // Enabled models grouped by provider for the small-model picker; providers
  // with nothing enabled disappear entirely.
  const roleOptions = pickerProviders
    .map((p) => ({
      ...p,
      models: p.models.filter((m) =>
        m.enabled && m.tool_call && (m.output_modalities?.includes('text') ?? true),
      ),
    }))
    .filter((p) => p.models.length > 0)
  const imageCatalog = pickerProviders.flatMap((provider) =>
    provider.models
      .filter((model) => model.output_modalities?.includes('image'))
      .map((model) => ({ provider, model })),
  )
  const imageRoleOptions = pickerProviders
    .map((provider) => ({
      ...provider,
      models: provider.models.filter((model) =>
        model.output_modalities?.includes('image') && model.capability_availability === 'supported',
      ),
    }))
    .filter((provider) => provider.models.length > 0)
  const imageIntegratedCount = imageRoleOptions.reduce(
    (count, provider) => count + provider.models.length,
    0,
  )
  // A configured ref whose model was since disabled/removed still renders (as
  // its raw ref, marked unavailable) so it can be seen and cleared.
  const smallModelListed =
    smallModel === '' || roleOptions.some((p) => p.models.some((m) => `${p.id}/${m.id}` === smallModel))
  const imageModelListed =
    imageModel === '' || imageRoleOptions.some((p) => p.models.some((m) => `${p.id}/${m.id}` === imageModel))

  return (
    <div>
      <div className="mb-5">
        <h3 className={`${SECTION_TITLE} mb-2`}>{t('settings.providers.roles.title')}</h3>
        <div className={ROW}>
          <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
            <BoltIcon className="h-4 w-4" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="text-[12px] font-medium text-[var(--color-foreground)]">
              {t('settings.providers.roles.smallName')}
            </div>
            <div className="text-[11px] text-[var(--color-muted-foreground)]">
              {roleOptions.length === 0
                ? t('settings.providers.roles.smallNoProviders')
                : t('settings.providers.roles.smallDesc')}
            </div>
            {smallError && (
              <div className="mt-1 text-[11px] text-[var(--color-destructive)]">
                {t('settings.providers.roles.smallSaveFailed', { reason: smallError })}
              </div>
            )}
            {smallSaved && (
              <div className="mt-1 text-[11px] text-[var(--color-success)]">
                {t('settings.providers.roles.smallSaved')}
              </div>
            )}
          </div>
          <select
            value={smallModel}
            disabled={smallSaving || (roleOptions.length === 0 && smallModel === '')}
            onChange={(e) => void changeSmallModel(e.target.value)}
            className={INPUT_SM}
            style={{ width: '15rem' }}
          >
            <option value="">{t('settings.providers.roles.smallUnset')}</option>
            {!smallModelListed && (
              <option value={smallModel}>
                {smallModel} — {t('settings.providers.roles.smallUnavailable')}
              </option>
            )}
            {roleOptions.map((p) => (
              <optgroup key={p.id} label={p.name || p.id}>
                {p.models.map((m) => (
                  <option key={m.id} value={`${p.id}/${m.id}`}>
                    {m.name || m.id}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        </div>
        <div className={`${ROW} mt-2`}>
          <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
            <PhotoIcon className="h-4 w-4" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-1.5 text-[12px] font-medium text-[var(--color-foreground)]">
              <span>{t('settings.providers.roles.imageName', { defaultValue: 'Image Model' })}</span>
              {imageCatalog.length > 0 && (
                <span className={CHIP}>
                  {t('settings.providers.roles.imageCounts', {
                    candidates: imageCatalog.length,
                    integrated: imageIntegratedCount,
                  })}
                </span>
              )}
            </div>
            <div className="text-[11px] text-[var(--color-muted-foreground)]">
              {imageRoleOptions.length === 0
                ? t('settings.providers.roles.imageNoProviders', { defaultValue: 'No integrated image-generation model is available.' })
                : t('settings.providers.roles.imageDesc', { defaultValue: 'Independent from the chat model and its provider. Select an available Image Model to let the Agent generate images. Calls may incur provider charges; Full access does not ask each time.' })}
            </div>
            {imageError && (
              <div className="mt-1 text-[11px] text-[var(--color-destructive)]">
                {t('settings.providers.roles.imageSaveFailed', {
                  reason: imageError,
                  defaultValue: `Could not save Image Model: ${imageError}`,
                })}
              </div>
            )}
            {imageSaved && (
              <div className="mt-1 text-[11px] text-[var(--color-success)]">
                {t('settings.providers.roles.imageSaved', { defaultValue: 'Image Model saved.' })}
              </div>
            )}
          </div>
          <select
            value={imageModel}
            disabled={imageSaving || (imageRoleOptions.length === 0 && imageModel === '')}
            onChange={(event) => void changeImageModel(event.target.value)}
            aria-label={t('settings.providers.roles.imageName', { defaultValue: 'Image Model' })}
            className={INPUT_SM}
            style={{ width: '15rem' }}
          >
            <option value="">{t('settings.providers.roles.imageUnset', { defaultValue: 'Not configured' })}</option>
            {!imageModelListed && (
              <option value={imageModel}>
                {imageModel} — {t('settings.providers.roles.imageUnavailable', { defaultValue: 'unavailable' })}
              </option>
            )}
            {imageRoleOptions.map((provider) => (
              <optgroup key={provider.id} label={provider.name || provider.id}>
                {provider.models.map((model) => (
                  <option key={model.id} value={`${provider.id}/${model.id}`}>
                    {model.name || model.id}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        </div>

      </div>

      <div className="mb-4 flex items-center justify-between">
        <div className="flex items-baseline gap-2">
          <h3 className={SECTION_TITLE}>{t('settings.providers.title')}</h3>
          <span className="font-mono text-[11px] text-[var(--color-muted-foreground)]">{providers.length}</span>
        </div>
        <button type="button" className={`${BTN_SECONDARY} ${BTN_SM}`} onClick={() => setAdding(true)}>
          <PlusIcon className="h-3.5 w-3.5" /> {t('settings.providers.addBtn')}
        </button>
      </div>

      {loading ? (
        <div className="animate-pulse py-6 text-center text-xs text-[var(--color-muted-foreground)]">{t('settings.providers.loadingHint')}</div>
      ) : providers.length === 0 ? (
        <EmptyState
          Icon={CpuChipIcon}
          title={t('settings.providers.noneConfigured')}
          hint={t('settings.providers.noneHint', { btn: t('settings.providers.addBtn') })}
        />
      ) : (
        <div className="space-y-2.5">
          {providers.map((p) => (
            <ProviderCard
              key={p.id}
              provider={p}
              catalog={catalogs[p.id] ?? []}
              catalogLoading={catalogLoading === p.id}
              activeProvider={activeProvider}
              activeModelName={activeProvider === p.id ? activeModel : ''}
              providerToolSaving={policySaving.has(p.id)}
              providerToolError={policyErrors[p.id]}
              selectedImageModel={imageModel.startsWith(`${p.id}/`) ? imageModel.slice(p.id.length + 1) : ''}
              modelForm={modelForm?.providerId === p.id ? modelForm : null}
              onRefreshCatalog={() => refreshCatalog(p.id)}
              onToggleModel={(mid) => toggleCatalogModel(p.id, mid)}
              onAddCustomModel={() => setModelForm({ providerId: p.id, target: null })}
              onEditCustomModel={(m) => setModelForm({ providerId: p.id, target: m })}
              onRemoveCustomModel={(mid) => removeCustomModel(p.id, mid)}
              onCancelModelForm={() => setModelForm(null)}
              onSaveCustomModel={saveCustomModel}
              onEdit={() => setEditing(p)}
              onDelete={() => deleteProvider(p.id)}
              onToggleWebSearch={() => void toggleProviderPolicy(p, 'web_search')}
            />
          ))}
        </div>
      )}
    </div>
  )
}

/** A single provider card: head (brand/name/url/actions) + model catalog. */
function ProviderCard({
  provider,
  catalog,
  catalogLoading,
  activeProvider,
  activeModelName,
  providerToolSaving,
  providerToolError,
  selectedImageModel,
  modelForm,
  onRefreshCatalog,
  onToggleModel,
  onAddCustomModel,
  onEditCustomModel,
  onRemoveCustomModel,
  onCancelModelForm,
  onSaveCustomModel,
  onEdit,
  onDelete,
  onToggleWebSearch,
}: {
  provider: ProviderDetail
  catalog: CatalogModel[]
  catalogLoading: boolean
  activeProvider: string
  activeModelName: string
  providerToolSaving: boolean
  providerToolError?: string
  selectedImageModel: string
  modelForm: { providerId: string; target: CustomModelDetail | null } | null
  onRefreshCatalog: () => void
  onToggleModel: (modelId: string) => void
  onAddCustomModel: () => void
  onEditCustomModel: (m: CustomModelDetail) => void
  onRemoveCustomModel: (modelId: string) => void
  onCancelModelForm: () => void
  onSaveCustomModel: (payload: {
    providerId: string
    id: string
    name?: string
    reasoning: boolean
    context: number
    attachment: boolean
    effortTiers: string[]
    isEdit: boolean
    originalId?: string
  }) => Promise<void>
  onEdit: () => void
  onDelete: () => void
  onToggleWebSearch: () => void
}) {
  const { t } = useTranslation()
  const [deleteConfirm, setDeleteConfirm] = useState(false)
  const [search, setSearch] = useState('')

  const webSearchCapability = provider.capabilities?.find((capability) => capability.id === 'web_search')
  const authAccount = resolveProviderAuthAccount(provider.auth_status, provider.auth_binding)
  const authMethodLabel = provider.auth_binding
    ? t(`settings.providers.auth.methods.${provider.auth_binding.method === 'codex_oauth'
        ? 'chatgpt'
        : provider.auth_binding.method === 'xai_oauth'
          ? 'grok'
          : 'copilot'}`)
    : ''
  const authNeedsReauth = !!provider.auth_binding && !!provider.auth_status
    && (!!authAccount?.requires_reauth || (!!provider.auth_binding.account_id && !authAccount))
  const authNeedsSignIn = !!provider.auth_binding && !!provider.auth_status && !authAccount && !authNeedsReauth
  const authSummary = provider.auth_binding
    ? provider.auth_status
      ? authAccount
        ? `${authNeedsReauth ? t('settings.providers.auth.needsReauth') : t('settings.providers.auth.connected')} · ${authAccount.login} · ${authMethodLabel}`
        : `${t('settings.providers.auth.signInRequired')} · ${authMethodLabel}`
      : `${t('settings.providers.auth.configured')} · ${authMethodLabel}`
    : provider.api_key_set
      ? `${t('settings.providers.auth.configured')} · ${t('settings.providers.auth.methods.apiKey')}`
      : t('settings.providers.auth.signInRequired')

  const addedCount = catalog.filter((m) => m.added).length
  const filtered = (() => {
    const q = search.trim().toLowerCase()
    if (!q) return catalog
    return catalog.filter(
      (m) => m.id.toLowerCase().includes(q) || (m.name ?? '').toLowerCase().includes(q),
    )
  })()

  return (
    <div
      className={`overflow-hidden rounded-[var(--radius-lg)] border bg-[var(--color-surface)] ${
        selectedImageModel
          ? 'border-[var(--color-primary)] shadow-[0_0_0_1px_var(--color-primary)]'
          : 'border-[var(--color-border)]'
      }`}
    >
      {/* Head */}
      <div className="flex items-center gap-3 border-b border-[var(--color-border)] px-3 py-2.5">
        <div className="grid h-[30px] w-[30px] shrink-0 place-items-center rounded-[var(--radius-md)] bg-[var(--color-secondary)] text-[var(--color-foreground)]">
          <ProviderIcon provider={provider.id} custom={provider.custom} size={18} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 text-[13px] font-semibold text-[var(--color-foreground)]">
            <span className="truncate">{provider.name || provider.id}</span>
            {activeProvider === provider.id && <span className={CHIP_ACCENT}>{t('settings.appearance.active')}</span>}
            {provider.custom && <span className={CHIP}>{t('settings.providers.custom')}</span>}
            {selectedImageModel && <span className={CHIP_ACCENT}>{t('settings.providers.roles.imageSelectedBadge')}</span>}
          </div>
          <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[11px] text-[var(--color-muted-foreground)]">
            <span
              className="h-1.5 w-1.5 shrink-0 rounded-full"
              style={{
                backgroundColor: authNeedsReauth || authNeedsSignIn
                  ? 'var(--color-warning-fg)'
                  : provider.auth_binding && authAccount
                    ? 'var(--color-success)'
                    : 'var(--color-muted-foreground)',
              }}
            />
            <span className="truncate">{authSummary}</span>
            {provider.base_url && (
              <span className="hidden truncate font-mono text-[10px] sm:inline">· {provider.base_url}</span>
            )}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          {deleteConfirm ? (
            activeModelName ? (
              <div className="flex max-w-[280px] flex-col gap-2 rounded-[var(--radius-md)] border border-[var(--color-warning-fg)] bg-[var(--color-warning-bg)] p-2.5 text-right">
                <div>
                  <div className="text-[12px] font-semibold text-[var(--color-warning-fg)]">{t('settings.providers.deleteInUseTitle')}</div>
                  <div className="mt-0.5 text-[10.5px] text-[var(--color-muted-foreground)]">
                    {t('settings.providers.deleteInUseBody', { model: activeModelName })}
                  </div>
                </div>
                <div className="flex flex-wrap justify-end gap-1.5">
                  <button type="button" className={`${BTN_GHOST} ${BTN_XS}`} onClick={() => setDeleteConfirm(false)}>
                    {t('common.cancel')}
                  </button>
                </div>
              </div>
            ) : (
              <>
                <button type="button" className={`${BTN_DANGER} ${BTN_XS}`} onClick={onDelete}>
                  {t('common.delete')}
                </button>
                <button type="button" className={`${BTN_GHOST} ${BTN_XS}`} onClick={() => setDeleteConfirm(false)}>
                  {t('common.cancel')}
                </button>
              </>
            )
          ) : (
            <>
              {(authNeedsReauth || authNeedsSignIn) && (
                <button type="button" className={`${BTN_SECONDARY} ${BTN_XS}`} onClick={onEdit}>
                  <ArrowPathIcon className="h-3.5 w-3.5" />
                  {authNeedsReauth ? t('settings.providers.auth.reauthenticate') : t('settings.providers.auth.signInAction')}
                </button>
              )}
              <button type="button" title={t('settings.providers.edit')} className={`${BTN_GHOST} ${BTN_XS}`} onClick={onEdit}>
                <PencilSquareIcon className="h-3.5 w-3.5" />
              </button>
              <button type="button" title={t('settings.providers.remove')} className={`${BTN_GHOST} ${BTN_XS}`} onClick={() => setDeleteConfirm(true)}>
                <TrashIcon className="h-3.5 w-3.5" />
              </button>
            </>
          )}
        </div>
      </div>

      {webSearchCapability && (
        <div className="flex items-center gap-3 border-b border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2.5">
          <GlobeAltIcon className="h-4 w-4 shrink-0 text-[var(--color-muted-foreground)]" />
          <div className="min-w-0 flex-1">
            <div className="text-[11px] font-medium text-[var(--color-foreground)]">
              {t('settings.providers.webSearchPolicy.title')}
            </div>
            <div className="text-[10px] text-[var(--color-muted-foreground)]">
              {t('settings.providers.webSearchPolicy.desc', {
                identity: [webSearchCapability.mechanism, webSearchCapability.model_label].filter(Boolean).join(' · '),
                turn: webSearchCapability.max_calls_per_turn,
                session: webSearchCapability.max_calls_per_session,
              })}
            </div>
            {providerToolError && (
              <div className="mt-0.5 text-[10px] text-[var(--color-destructive)]">{providerToolError}</div>
            )}
          </div>
          <Switch
            on={!!provider.provider_tools?.web_search?.enabled}
            disabled={providerToolSaving || (!provider.provider_tools?.web_search?.enabled && webSearchCapability.availability !== 'supported')}
            onClick={onToggleWebSearch}
            ariaLabel={t('settings.providers.webSearchPolicy.title')}
            title={webSearchCapability.availability !== 'supported' && !provider.provider_tools?.web_search?.enabled
              ? t('settings.providers.webSearchPolicy.unavailable')
              : provider.provider_tools?.web_search?.enabled
              ? t('settings.providers.webSearchPolicy.disable')
              : t('settings.providers.webSearchPolicy.enable')}
          />
        </div>
      )}

      {/* Models sub-area */}
      <div className="px-3 pb-3 pt-2">
        <div className="flex flex-wrap items-center justify-between gap-2 pb-2">
          <span className="text-[10.5px] text-[var(--color-muted-foreground)]">
            <b className="font-semibold text-[var(--color-foreground)]">{addedCount}</b>/{catalog.length} {t('settings.providers.models')}
          </span>
          <div className="ml-auto flex items-center gap-1.5">
            <button
              type="button"
              className={`${BTN_SECONDARY} ${BTN_XS}`}
              onClick={onRefreshCatalog}
              title={t('settings.providers.refreshCatalog')}
            >
              ↻
            </button>
            <button type="button" className={`${BTN_SECONDARY} ${BTN_XS}`} onClick={onAddCustomModel}>
              <PlusIcon className="h-3.5 w-3.5" /> {t('settings.providers.addCustomModel')}
            </button>
          </div>
        </div>

        {/* Inline catalog (open by default) */}
        <div className="mt-1.5 border-t border-dashed border-[var(--color-border)] pt-2">
          {catalogLoading && catalog.length === 0 ? (
            <div className="animate-pulse py-3 text-center text-[10px] text-[var(--color-muted-foreground)]">
              {t('settings.providers.loadingHint')}
            </div>
          ) : (
            <>
              <input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                type="text"
                placeholder={t('settings.providers.catalogSearch')}
                className={INPUT_SM + ' mb-1.5'}
              />
              {filtered.length === 0 ? (
                <div className="py-3 text-center text-[10px] text-[var(--color-muted-foreground)]">
                  {provider.custom ? t('settings.providers.noCatalog') : t('settings.providers.allConfigured')}
                </div>
              ) : (
                <div className="max-h-[220px] overflow-y-auto rounded-[var(--radius-md)] border border-[var(--color-border)] bg-[var(--color-background)] p-0.5">
                  {filtered.map((cm) => (
                    <div
                      key={cm.id}
                      className="flex items-center gap-2.5 rounded-[var(--radius-sm)] px-2 py-1.5 hover:bg-[var(--color-secondary)]"
                    >
                      <div className="min-w-0 flex-1">
                        <div className="font-mono text-[11px] text-[var(--color-foreground)]">{cm.id}</div>
                        <div className="truncate text-[10px] text-[var(--color-muted-foreground)]">
                          {cm.name || cm.id}
                          {cm.context ? ` · ${formatContext(cm.context)}` : ''}
                          {cm.reasoning ? ` · ${t('settings.providers.customReasoning')}` : ''}
                          {cm.attachment ? ` · ${t('settings.providers.supportImage')}` : ''}
                        </div>
                      </div>
                      {cm.added && cm.custom ? (
                        <div className="flex items-center gap-1">
                          <button
                            type="button"
                            title={t('settings.providers.editModel')}
                            className={`${BTN_GHOST} ${BTN_XS}`}
                            onClick={() => onEditCustomModel(cmToDetail(cm))}
                          >
                            <PencilSquareIcon className="h-3.5 w-3.5" />
                          </button>
                          <button
                            type="button"
                            title={t('settings.providers.remove')}
                            className={`${BTN_GHOST} ${BTN_XS} text-[var(--color-muted-foreground)] hover:text-[var(--color-destructive)]`}
                            onClick={() => onRemoveCustomModel(cm.id)}
                          >
                            <TrashIcon className="h-3.5 w-3.5" />
                          </button>
                        </div>
                      ) : (
                        <Switch
                          on={!!cm.added}
                          onClick={() => onToggleModel(cm.id)}
                          title={cm.added ? t('settings.providers.hideModel') : t('settings.providers.showModel')}
                        />
                      )}
                    </div>
                  ))}
                </div>
              )}
            </>
          )}
        </div>

        {/* Inline custom-model form */}
        {modelForm && (
          <CustomModelForm
            target={modelForm.target}
            onCancel={onCancelModelForm}
            onSave={(payload) =>
              onSaveCustomModel({ ...payload, providerId: provider.id, originalId: payload.originalId })
            }
          />
        )}
      </div>
    </div>
  )
}

export function buildImageEndpointConfig(
  enabled: boolean,
  baseURL: string,
  drafts: { id: string; name: string; sizes: string }[],
  assetHosts: string,
): ImageEndpointConfig | undefined {
  if (!enabled) return undefined
  const models = drafts.map((model) => ({
    id: model.id.trim(),
    name: model.name.trim() || undefined,
    sizes: model.sizes.split(',').map((size) => size.trim()).filter(Boolean),
  }))
  if (!baseURL.trim()) throw new Error('Image endpoint URL is required.')
  if (models.length === 0 || models.some((model) => !model.id)) {
    throw new Error('Every image model needs an ID.')
  }
  return {
    protocol: 'openai_images',
    base_url: baseURL.trim(),
    models,
    asset_hosts: assetHosts.split(',').map((host) => host.trim()).filter(Boolean),
  }
}

// Provider updates use null as the explicit "restore registry default" signal.
// Omission preserves the stored value, which remains important for older
// clients and masked secret-style edit forms.
export function buildProviderBaseURLUpdate(
  storedBaseURL: string | undefined,
  draftBaseURL: string,
): string | null | undefined {
  const normalized = draftBaseURL.trim()
  if (normalized) return normalized
  return storedBaseURL?.trim() ? null : undefined
}

/** Add/edit provider form with advanced chat config and an independent image endpoint. */
function ProviderForm({
  editing,
  setupList,
  configuredIds,
  onCancel,
  onSaved,
  onAuthenticated,
}: {
  editing: ProviderDetail | null
  setupList: SetupProvider[]
  configuredIds: string[]
  onCancel: () => void
  onSaved: () => void
  onAuthenticated: (providerId: string, status: ProviderAuthStatus) => void | Promise<void>
}) {
  const { t } = useTranslation()
  const isEdit = !!editing
  const initialAuthMethods = editing?.custom
    ? ['api_key' as const]
    : providerCredentialMethods(
        setupList.find((provider) => provider.id === editing?.id)?.auth_methods ?? editing?.auth_methods,
        editing?.auth_binding,
      )
  const initialAuthMethod = editing?.auth_binding?.method ?? initialAuthMethods[0]
  const [mode, setMode] = useState<'registry' | 'custom'>(editing?.custom ? 'custom' : 'registry')
  const [selId, setSelId] = useState('')
  const [customId, setCustomId] = useState(editing?.id ?? '')
  const [name, setName] = useState(editing?.name ?? '')
  const [apiKey, setApiKey] = useState('')
  const [authMethod, setAuthMethod] = useState<ProviderCredentialMethod>(initialAuthMethod)
  const [authBinding, setAuthBinding] = useState<ProviderAuthBinding | null>(
    initialAuthMethod === 'api_key' ? null : editing?.auth_binding ?? { method: initialAuthMethod },
  )
  const [authStatus, setAuthStatus] = useState<ProviderAuthStatus | undefined>(editing?.auth_status)
  const [baseUrl, setBaseUrl] = useState(editing?.base_url ?? '')
  const [headers, setHeaders] = useState<{ key: string; value: string }[]>(
    Object.entries(editing?.headers ?? {}).map(([key, value]) => ({ key, value })),
  )
  // Thinking is a tri-state override for the qwen3-style enable_thinking
  // request kwarg — only meaningful for custom (self-hosted) endpoints, so it
  // is only shown and sent for custom providers. '' (Default) omits the field
  // so the backend keeps nil and never sends the kwarg. Vision has no form
  // control at all: image support is per-model metadata (registry modalities /
  // custom-model attachment), and a provider-level override only served to
  // silently strip images. Both fields use update-by-replacement on the
  // backend, so simply not sending them clears any stale stored override.
  const [thinking, setThinking] = useState<'' | 'on' | 'off'>(
    editing?.thinking === true ? 'on' : editing?.thinking === false ? 'off' : '',
  )
  const [reasoningEffort, setReasoningEffort] = useState(editing?.reasoning_effort ?? '')
  const [imageEndpointEnabled, setImageEndpointEnabled] = useState(!!editing?.image_endpoint)
  const [imageEndpointBaseURL, setImageEndpointBaseURL] = useState(editing?.image_endpoint?.base_url ?? '')
  const [imageEndpointModels, setImageEndpointModels] = useState(
    (editing?.image_endpoint?.models?.length ? editing.image_endpoint.models : [{ id: '', name: '', sizes: ['1024x1024'] }])
      .map((model) => ({
        id: model.id,
        name: model.name ?? '',
        sizes: (model.sizes ?? []).join(', '),
      })),
  )
  const [imageAssetHosts, setImageAssetHosts] = useState((editing?.image_endpoint?.asset_hosts ?? []).join(', '))
  const [advancedOpen, setAdvancedOpen] = useState(
    !!(editing?.base_url || editing?.image_endpoint || (editing?.headers && Object.keys(editing.headers).length)),
  )
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  // Filter out already-configured providers from the registry picker (add mode).
  const availableSetup = setupList.filter((s) => !configuredIds.includes(s.id))

  const providerId = isEdit ? editing!.id : mode === 'custom' ? customId.trim() : selId
  // Custom (non-registry) providers get the enable_thinking knob; registry
  // providers derive everything from models.dev metadata.
  const isCustomProvider = isEdit ? !!editing?.custom : mode === 'custom'
  const selectedSetup = setupList.find((provider) => provider.id === providerId)
  const authMethods = isCustomProvider
    ? ['api_key' as const]
    : providerCredentialMethods(selectedSetup?.auth_methods ?? editing?.auth_methods, editing?.auth_binding)
  const isManagedAuth = authMethod !== 'api_key'
  const managedAuthReady = authMethod === 'api_key' || isProviderAuthReady(authStatus, authBinding)

  useEffect(() => {
    if (isEdit) return
    const setup = setupList.find((provider) => provider.id === selId)
    const methods = mode === 'custom'
      ? ['api_key' as const]
      : providerCredentialMethods(setup?.auth_methods)
    const next = methods.includes('api_key') ? 'api_key' : methods[0]
    setAuthMethod(next)
    setAuthBinding(next === 'api_key' ? null : { method: next })
    setAuthStatus(undefined)
    setApiKey('')
  }, [isEdit, mode, selId, setupList])

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    if (!providerId) {
      setError(t('settings.providers.customIdRequired'))
      return
    }
    if (authMethod === 'api_key' && !apiKey.trim() && (!isEdit || !editing?.api_key_set)) {
      setError(t('settings.providers.enterApiKey'))
      return
    }
    if (authMethod !== 'api_key' && !isProviderAuthReady(authStatus, authBinding)) {
      setError(t('settings.providers.auth.signInRequired'))
      return
    }
    setSaving(true)
    try {
      const builtHeaders = isManagedAuth ? {} : buildHeaders(headers)
      let imageEndpoint: ImageEndpointConfig | null | undefined = buildImageEndpointConfig(
        imageEndpointEnabled, imageEndpointBaseURL, imageEndpointModels, imageAssetHosts,
      )
      if (isManagedAuth) {
        imageEndpoint = isEdit && editing?.image_endpoint ? null : undefined
      } else if (!imageEndpointEnabled && isEdit && editing?.image_endpoint) {
        imageEndpoint = null
      }
      // '' (Default) → undefined so the JSON omits the override entirely.
      // Vision is never sent: image support comes from model metadata, and
      // omitting the field clears any stale stored override on save.
      const thinkingOverride = !isCustomProvider || thinking === '' ? undefined : thinking === 'on'
      if (isEdit) {
        const data: Parameters<typeof api.updateProvider>[1] = {
          name: name || undefined,
          ...(!isManagedAuth ? {
            base_url: buildProviderBaseURLUpdate(editing?.base_url, baseUrl),
            headers: Object.keys(builtHeaders).length ? builtHeaders : undefined,
          } : {}),
          thinking: thinkingOverride,
          reasoning_effort: reasoningEffort || undefined,
          image_endpoint: imageEndpoint,
          auth_binding: authMethod === 'api_key'
            ? editing?.auth_binding ? null : undefined
            : authBinding ?? { method: authMethod },
        }
        if (authMethod === 'api_key' && apiKey.trim()) data.api_key = apiKey.trim()
        await api.updateProvider(editing!.id, data)
      } else {
        await api.addProvider({
          id: providerId,
          api_key: authMethod === 'api_key' ? apiKey.trim() : undefined,
          auth_binding: authMethod === 'api_key' ? undefined : authBinding ?? { method: authMethod },
          name: name || undefined,
          thinking: thinkingOverride,
          reasoning_effort: reasoningEffort || undefined,
          ...(!isManagedAuth ? {
            base_url: baseUrl.trim() || undefined,
            headers: Object.keys(builtHeaders).length ? builtHeaders : undefined,
            image_endpoint: imageEndpoint ?? undefined,
          } : {}),
        })
      }
      onSaved()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.providers.connectFailed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form onSubmit={save}>
      <div className="mb-4 flex items-center justify-between">
        <h3 className={SECTION_TITLE}>{isEdit ? t('settings.providers.editProvider') : t('settings.providers.add')}</h3>
        <button type="button" className={`${BTN_GHOST} ${BTN_SM}`} onClick={onCancel}>
          ← {t('common.back')}
        </button>
      </div>

      <div className="rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)]">
        <div className="border-b border-[var(--color-border)] bg-[var(--color-muted)] px-3.5 py-2.5 text-[11px] font-semibold uppercase tracking-wide text-[var(--color-muted-foreground)]">
          {isEdit ? editing!.name || editing!.id : t('settings.providers.add')}
        </div>
        <div className="p-4">
          {/* Provider selection (add mode only) */}
          {!isEdit && (
            <Field label={t('settings.providers.selectProvider')}>
              <Segmented
                value={mode}
                onChange={(m) => setMode(m)}
                options={[
                  { value: 'registry', label: t('settings.providers.selectProvider') },
                  { value: 'custom', label: t('settings.providers.custom') },
                ]}
              />
              <div className="mt-2">
                {mode === 'registry' ? (
                  <select
                    value={selId}
                    onChange={(e) => setSelId(e.target.value)}
                    className={INPUT}
                  >
                    <option value="">{t('settings.providers.selectProvider')}</option>
                    {availableSetup.map((s) => (
                      <option key={s.id} value={s.id}>
                        {s.name}
                      </option>
                    ))}
                  </select>
                ) : (
                  <input
                    value={customId}
                    onChange={(e) => setCustomId(e.target.value)}
                    type="text"
                    placeholder={t('settings.providers.customIdPlaceholder')}
                    className={INPUT_MONO}
                  />
                )}
              </div>
            </Field>
          )}

          <ProviderAuthSection
            methods={authMethods}
            value={authMethod}
            binding={authBinding}
            initialStatus={authStatus}
            disabled={saving}
            apiKeyField={(
              <input
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                type="password"
                aria-label={t('settings.providers.apiKey')}
                placeholder={isEdit ? t('settings.providers.apiKeyUnchanged') : t('setup.apiKeyPlaceholder')}
                className={INPUT_MONO}
              />
            )}
            onMethodChange={setAuthMethod}
            onBindingChange={setAuthBinding}
            onStatusChange={setAuthStatus}
            onAuthenticated={(status) => onAuthenticated(providerId, status)}
          />

          {(mode === 'custom' || isEdit) && (
            <Field label={t('settings.providers.customName')}>
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                type="text"
                placeholder={t('settings.providers.customNamePlaceholder')}
                className={INPUT}
              />
            </Field>
          )}

          {/* Advanced disclosure */}
          <button
            type="button"
            onClick={() => setAdvancedOpen((v) => !v)}
            className="mb-3 flex items-center gap-1 text-[11px] font-medium text-[var(--color-muted-foreground)] hover:text-[var(--color-foreground)]"
            aria-expanded={advancedOpen}
            aria-controls="provider-advanced-fields"
          >
            <ChevronDownIcon
              className="h-3.5 w-3.5 transition-transform motion-reduce:transition-none"
              style={{ transform: advancedOpen ? 'rotate(180deg)' : 'none' }}
            />
            {t('settings.providers.advanced')}
          </button>

          {advancedOpen && (
            <div id="provider-advanced-fields" className="mb-3 rounded-[var(--radius-md)] border border-[var(--color-border)] bg-[var(--color-muted)] p-3">
              {!isManagedAuth && (
                <>
                  <Field label={t('settings.providers.endpoint')}>
                    <input
                      value={baseUrl}
                      onChange={(e) => setBaseUrl(e.target.value)}
                      type="text"
                      placeholder={t('settings.providers.endpointPlaceholder')}
                      className={INPUT_MONO}
                    />
                  </Field>

                  <div className="mb-3.5">
                    <div className="mb-1.5 flex items-center justify-between">
                      <label className={LABEL + ' !mb-0'}>{t('settings.providers.headers')}</label>
                      <button
                        type="button"
                        className={`${BTN_GHOST} ${BTN_XS}`}
                        onClick={() => setHeaders((h) => [...h, { key: '', value: '' }])}
                      >
                        + {t('settings.providers.addHeader')}
                      </button>
                    </div>
                    {headers.length === 0 && (
                      <div className="text-[11px] text-[var(--color-muted-foreground)]">{t('settings.providers.headersHint')}</div>
                    )}
                    {headers.map((h, i) => (
                      <div key={i} className="mb-2 flex gap-2">
                        <input
                          value={h.key}
                          onChange={(e) => setHeaders((prev) => prev.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))}
                          type="text"
                          placeholder={t('settings.providers.headerKey')}
                          className={INPUT_MONO}
                        />
                        <input
                          value={h.value}
                          onChange={(e) => setHeaders((prev) => prev.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))}
                          type="text"
                          placeholder="value"
                          className={INPUT_MONO}
                        />
                        <button
                          type="button"
                          onClick={() => setHeaders((prev) => prev.filter((_, j) => j !== i))}
                          className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-sm)] border border-[var(--color-border)] text-[var(--color-muted-foreground)] hover:bg-[var(--color-secondary)]"
                        >
                          ✕
                        </button>
                      </div>
                    ))}
                  </div>
                </>
              )}

              <Field label={t('settings.providers.advanced')}>
                <div className="space-y-2">
                  {isCustomProvider && (
                    <div className="flex items-center justify-between rounded-[var(--radius-md)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2">
                      <div>
                        <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.providers.customReasoning')}</div>
                        <div className="text-[10px] text-[var(--color-muted-foreground)]">{t('settings.providers.customReasoningDesc')}</div>
                      </div>
                      <select
                        value={thinking}
                        onChange={(e) => setThinking(e.target.value as '' | 'on' | 'off')}
                        className={INPUT_SM}
                        style={{ width: '8rem' }}
                      >
                        <option value="">{t('common.default')}</option>
                        <option value="on">{t('common.on')}</option>
                        <option value="off">{t('common.off')}</option>
                      </select>
                    </div>
                  )}
                  <div className="flex items-center justify-between rounded-[var(--radius-md)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2">
                    <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.providers.supportReasoning')}</div>
                    <select
                      value={reasoningEffort}
                      onChange={(e) => setReasoningEffort(e.target.value)}
                      className={INPUT_SM}
                      style={{ width: '8rem' }}
                    >
                      <option value="">{t('common.default')}</option>
                      <option value="low">{t('common.low')}</option>
                      <option value="medium">{t('common.medium')}</option>
                      <option value="high">{t('common.high')}</option>
                    </select>
                  </div>
                </div>
              </Field>

              {!isManagedAuth && <div className="mt-3 rounded-[var(--radius-md)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3">
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <div className="text-[12px] font-medium text-[var(--color-foreground)]">
                      {t('settings.providers.imageEndpoint.title')}
                    </div>
                    <div className="text-[10px] text-[var(--color-muted-foreground)]">
                      {t('settings.providers.imageEndpoint.desc')}
                    </div>
                  </div>
                  <Switch
                    on={imageEndpointEnabled}
                    onClick={() => setImageEndpointEnabled((enabled) => !enabled)}
                    ariaLabel={t('settings.providers.imageEndpoint.configure')}
                  />
                </div>
                {imageEndpointEnabled && (
                  <div className="mt-3 space-y-3 border-t border-[var(--color-border)] pt-3">
                    <Field label={t('settings.providers.imageEndpoint.protocol')}>
                      <select value="openai_images" disabled className={INPUT_SM}>
                        <option value="openai_images">OpenAI Images · POST /images/generations</option>
                      </select>
                    </Field>
                    <Field label={t('settings.providers.imageEndpoint.baseUrl')}>
                      <input
                        value={imageEndpointBaseURL}
                        onChange={(event) => setImageEndpointBaseURL(event.target.value)}
                        type="url"
                        placeholder="https://api.example.com/v1"
                        className={INPUT_MONO}
                      />
                    </Field>
                    <div>
                      <div className="mb-1.5 flex items-center justify-between">
                        <label className={LABEL + ' !mb-0'}>{t('settings.providers.imageEndpoint.models')}</label>
                        <button
                          type="button"
                          className={`${BTN_GHOST} ${BTN_XS}`}
                          onClick={() => setImageEndpointModels((models) => [...models, { id: '', name: '', sizes: '1024x1024' }])}
                        >
                          {t('settings.providers.imageEndpoint.addModel')}
                        </button>
                      </div>
                      <div className="space-y-2">
                        {imageEndpointModels.map((model, index) => (
                          <div key={index} className="grid grid-cols-[1fr_1fr_1fr_auto] gap-2">
                            <input
                              value={model.id}
                              onChange={(event) => setImageEndpointModels((models) => models.map((item, itemIndex) => itemIndex === index ? { ...item, id: event.target.value } : item))}
                              placeholder="model-id"
                              aria-label={t('settings.providers.imageEndpoint.modelId', { index: index + 1 })}
                              className={INPUT_MONO}
                            />
                            <input
                              value={model.name}
                              onChange={(event) => setImageEndpointModels((models) => models.map((item, itemIndex) => itemIndex === index ? { ...item, name: event.target.value } : item))}
                              placeholder={t('settings.providers.imageEndpoint.displayName')}
                              aria-label={t('settings.providers.imageEndpoint.modelName', { index: index + 1 })}
                              className={INPUT}
                            />
                            <input
                              value={model.sizes}
                              onChange={(event) => setImageEndpointModels((models) => models.map((item, itemIndex) => itemIndex === index ? { ...item, sizes: event.target.value } : item))}
                              placeholder="1024x1024, 1792x1024"
                              aria-label={t('settings.providers.imageEndpoint.modelSizes', { index: index + 1 })}
                              className={INPUT_MONO}
                            />
                            <button
                              type="button"
                              className={`${BTN_GHOST} ${BTN_XS}`}
                              onClick={() => setImageEndpointModels((models) => models.filter((_, itemIndex) => itemIndex !== index))}
                              aria-label={t('settings.providers.imageEndpoint.removeModel', { index: index + 1 })}
                            >
                              <TrashIcon className="h-3.5 w-3.5" />
                            </button>
                          </div>
                        ))}
                      </div>
                    </div>
                    <Field label={t('settings.providers.imageEndpoint.assetHosts')}>
                      <input
                        value={imageAssetHosts}
                        onChange={(event) => setImageAssetHosts(event.target.value)}
                        placeholder="cdn.example.com, *.assets.example.com"
                        className={INPUT_MONO}
                      />
                      <div className="mt-1 text-[10px] text-[var(--color-muted-foreground)]">
                        {t('settings.providers.imageEndpoint.assetHostsHint')}
                      </div>
                    </Field>
                  </div>
                )}
              </div>}
            </div>
          )}

          {error && <div className="mb-3 text-[11px] text-[var(--color-destructive)]">{error}</div>}
        </div>
        <div className="flex justify-end gap-2 border-t border-[var(--color-border)] bg-[var(--color-muted)] px-3.5 py-3">
          <button type="button" className={BTN_SECONDARY} onClick={onCancel}>
            {t('common.cancel')}
          </button>
          <button type="submit" disabled={saving || !managedAuthReady} className={BTN_PRIMARY}>
            {saving ? t('settings.providers.saving') : isEdit ? t('settings.mcp.save') : t('settings.providers.addBtn')}
          </button>
        </div>
      </div>
    </form>
  )
}

/** Inline add/edit form for a single custom model. */
function CustomModelForm({
  target,
  onCancel,
  onSave,
}: {
  target: CustomModelDetail | null
  onCancel: () => void
  onSave: (payload: {
    id: string
    name?: string
    reasoning: boolean
    context: number
    attachment: boolean
    effortTiers: string[]
    isEdit: boolean
    originalId?: string
  }) => Promise<void>
}) {
  const { t } = useTranslation()
  const isEdit = !!target
  const [id, setId] = useState(target?.id ?? '')
  const [name, setName] = useState(target?.name ?? '')
  const [context, setContext] = useState(target?.context ? String(target.context) : '')
  const [reasoning, setReasoning] = useState(!!target?.reasoning)
  const [attachment, setAttachment] = useState(!!target?.attachment)
  const [tiersText, setTiersText] = useState((target?.effort_tiers ?? []).join(', '))
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    if (!id.trim()) {
      setError(t('settings.providers.customModelRequired'))
      return
    }
    setSaving(true)
    try {
      await onSave({
        id: id.trim(),
        name: name.trim() || undefined,
        reasoning,
        context: context ? Number(context) : 0,
        attachment,
        effortTiers: parseTiers(tiersText),
        isEdit,
        originalId: target?.id,
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.providers.connectFailed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form
      onSubmit={save}
      className="mt-2 rounded-[var(--radius-md)] border border-[var(--color-border)] bg-[var(--color-muted)] p-3"
    >
      <div className="mb-2 text-[11.5px] font-semibold text-[var(--color-foreground)]">
        {isEdit ? t('settings.providers.editModel') : t('settings.providers.addModel')}
      </div>
      <div className="grid grid-cols-2 gap-2.5">
        <Field label={t('settings.providers.customModelId')}>
          <input value={id} onChange={(e) => setId(e.target.value)} type="text" placeholder="gpt-4o" className={INPUT_MONO} />
        </Field>
        <Field label={t('settings.providers.customName')}>
          <input value={name} onChange={(e) => setName(e.target.value)} type="text" placeholder="GPT-4o" className={INPUT} />
        </Field>
        <Field label={t('settings.providers.contextWindow')}>
          <input
            value={context}
            onChange={(e) => setContext(e.target.value)}
            type="number"
            placeholder="128000"
            className={INPUT}
          />
        </Field>
        <Field label={t('settings.providers.effortTiers')}>
          <input
            value={tiersText}
            onChange={(e) => setTiersText(e.target.value)}
            type="text"
            placeholder="low, medium, high"
            className={INPUT_MONO}
          />
        </Field>
      </div>
      <div className="mb-2 flex items-center gap-4">
        <label className="flex items-center gap-2 text-[11px] text-[var(--color-foreground)]">
          <Switch on={reasoning} onClick={() => setReasoning((v) => !v)} /> {t('settings.providers.customReasoning')}
        </label>
        <label className="flex items-center gap-2 text-[11px] text-[var(--color-foreground)]">
          <Switch on={attachment} onClick={() => setAttachment((v) => !v)} /> {t('settings.providers.supportImage')}
        </label>
      </div>
      {error && <div className="mb-2 text-[11px] text-[var(--color-destructive)]">{error}</div>}
      <div className="flex justify-end gap-2">
        <button type="button" className={`${BTN_GHOST} ${BTN_SM}`} onClick={onCancel}>
          {t('common.cancel')}
        </button>
        <button type="submit" disabled={saving} className={`${BTN_PRIMARY} ${BTN_SM}`}>
          {saving ? t('settings.providers.saving') : t('settings.mcp.save')}
        </button>
      </div>
    </form>
  )
}

// ════════════════════════════════════════════════════════════════════════════
// General tab — default mode, auto-approve, max iterations, language
// (matches the Vue SettingsDialog 'general' tab; a functional port.)
// ════════════════════════════════════════════════════════════════════════════

function GeneralTab() {
  const dispatch = useAppDispatch()
  const { t, i18n } = useTranslation()
  const autoApprove = useAppSelector((s) => s.model.autoApprove)
  const currentTaskID = useAppSelector((s) => s.session.currentSessionId)
  const currentTaskIDRef = useRef(currentTaskID)
  currentTaskIDRef.current = currentTaskID
  const wsConnected = useAppSelector((s) => s.session.wsConnected)
  const tokenSnapshot = useAppSelector((s) => s.chat.tokenSnapshot)
  const bleAvailable = useAppSelector((s) => s.ui.bleAvailable)
  const bleEnabled = useAppSelector((s) => s.ui.bleEnabled)
  const [bleSaving, setBLESaving] = useState(false)

  const tokenPct = tokenSnapshot?.model_context_limit
    ? Math.round((tokenSnapshot.total_tokens / tokenSnapshot.model_context_limit) * 100)
    : 0

  async function toggleAutoApprove() {
    const next = !autoApprove
    try {
      await api.setApprovalMode(next, currentTaskID || undefined)
      if (currentTaskIDRef.current !== currentTaskID) return
      dispatch(modelActions.setAutoApprove(next))
      dispatch(modelActions.setMode(next ? 'full_access' : 'approval'))
    } catch (err) {
      console.error('Failed to toggle auto-approve:', err)
    }
  }

  async function toggleBLE() {
    if (bleSaving) return
    setBLESaving(true)
    const next = !bleEnabled
    try {
      const res = await api.setChannelBLE(next)
      dispatch(uiActions.setBLEState({ available: bleAvailable, enabled: res.enabled }))
    } catch (err) {
      console.error('Failed to toggle BLE:', err)
    } finally {
      setBLESaving(false)
    }
  }

  async function changeLanguage(code: string) {
    await api.setAccountPreferences({ language: code })
    await setLocale(code as SupportedLocale)
  }

  return (
    <div className="space-y-5">
      <h3 className={SECTION_TITLE}>{t('settings.tabs.general')}</h3>

      <div className={ROW}>
        <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
          <span
            className="h-2 w-2 rounded-full"
            style={{ backgroundColor: wsConnected ? 'var(--color-success)' : 'var(--color-border)' }}
          />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.general.serverState')}</div>
          <div
            className="text-[11px]"
            style={{ color: wsConnected ? 'var(--color-success)' : 'var(--color-muted-foreground)' }}
          >
            {wsConnected ? t('settings.general.serverOnline') : t('settings.general.serverOffline')}
          </div>
        </div>
      </div>

      {/* Desktop only: current app version + manual update check. The banner
          is the install surface; this row lets the user re-check after
          dismissing it. */}
      {isTauri && <VersionUpdateRow />}

      {tokenSnapshot && (
        <div className={ROW}>
          <div className="min-w-0 flex-1">
            <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.general.tokenUsage')}</div>
            <div className="mt-1.5 flex items-center gap-2">
              <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-[var(--color-muted)]">
                <div
                  className="h-full rounded-full transition-all"
                  style={{
                    width: `${tokenPct}%`,
                    backgroundColor: tokenPct > 80 ? 'var(--color-destructive)' : tokenPct > 50 ? 'var(--color-warning-fg)' : 'var(--color-accent-neutral)',
                  }}
                />
              </div>
              <span className="font-mono text-[10px] text-[var(--color-muted-foreground)]">
                {tokenSnapshot.total_tokens.toLocaleString()}
                {tokenSnapshot.model_context_limit ? ` / ${tokenSnapshot.model_context_limit.toLocaleString()}` : ''}
              </span>
            </div>
          </div>
        </div>
      )}

      <div className="text-[10px] font-medium uppercase tracking-wider text-[var(--color-muted-foreground)]">
        {t('settings.general.preferences')}
      </div>

      {/* Auto-approve */}
      <div className={ROW}>
        <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
          <ShieldCheckIcon className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.general.autoApproveTitle')}</div>
          <div className="text-[11px] text-[var(--color-muted-foreground)]">
            {t('settings.general.autoApproveDesc')}
          </div>
        </div>
        <Switch on={autoApprove} onClick={toggleAutoApprove} title={autoApprove ? t('common.disable') : t('common.enable')} />
      </div>

      {bleAvailable && (
        <div className={ROW}>
          <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
            <BoltIcon className="h-4 w-4" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.general.bleTitle')}</div>
            <div className="text-[11px] text-[var(--color-muted-foreground)]">{t('settings.general.bleDesc')}</div>
          </div>
          <Switch
            on={bleEnabled}
            onClick={toggleBLE}
            disabled={bleSaving}
            title={bleEnabled ? t('common.disable') : t('common.enable')}
          />
        </div>
      )}

      {/* Language */}
      <div className={ROW}>
        <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
          <GlobeAltIcon className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.general.languageTitle')}</div>
          <div className="text-[11px] text-[var(--color-muted-foreground)]">{t('settings.general.languageDesc')}</div>
        </div>
        <select
          value={i18n.language}
          onChange={(e) => changeLanguage(e.target.value)}
          className={INPUT_SM}
          style={{ width: '9rem' }}
        >
          {SUPPORTED_LOCALES.map((locale) => (
            <option key={locale} value={locale}>
              {LOCALE_LABELS[locale]}
            </option>
          ))}
        </select>
      </div>
    </div>
  )
}

function VersionUpdateRow() {
  const { t } = useTranslation()
  const { status, version, check } = useAppUpdate()
  const [appVersion, setAppVersion] = useState('')

  // The Tauri app version (capability core:app:allow-version), not the sidecar
  // version — the updater compares against this one.
  useEffect(() => {
    if (!isTauri) return
    let cancelled = false
    import('@tauri-apps/api/app')
      .then(({ getVersion }) => getVersion())
      .then((v) => {
        if (!cancelled) setAppVersion(v)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [])

  const busy = status === 'checking' || status === 'downloading' || status === 'restarting'
  const hint =
    status === 'checking'
      ? t('update.checking')
      : status === 'up-to-date'
        ? t('update.upToDate')
        : status === 'available'
          ? t('update.newVersionFound', { version })
          : status === 'downloading'
            ? t('update.downloading')
            : status === 'restarting'
              ? t('update.restarting')
              : status === 'error'
                ? t('update.checkFailed')
                : ''

  return (
    <div className={ROW}>
      <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
        <ArrowPathIcon className="h-4 w-4" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('update.sectionTitle')}</div>
        <div className="text-[11px] text-[var(--color-muted-foreground)]">
          {appVersion ? `${t('update.currentVersion')} v${appVersion}` : ''}
          {hint ? (appVersion ? ` · ${hint}` : hint) : ''}
        </div>
      </div>
      <button
        type="button"
        onClick={() => void check({ manual: true })}
        disabled={busy}
        className={`${BTN_SECONDARY} ${BTN_XS}`}
      >
        {t('update.checkButton')}
      </button>
    </div>
  )
}

function AgentTab() {
  const { t } = useTranslation()

  return (
    <div className="space-y-5">
      <h3 className={SECTION_TITLE}>{t('settings.tabs.agent')}</h3>
      <ToolSearchSection />
    </div>
  )
}

function ReviewTab() {
  const { t } = useTranslation()

  return (
    <div className="space-y-5">
      <h3 className={SECTION_TITLE}>{t('settings.tabs.review')}</h3>
      <ApprovalReviewSection />
    </div>
  )
}

function ToolSearchSection() {
  const { t } = useTranslation()
  const [status, setStatus] = useState<ToolSearchStatusResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const load = useCallback(() => {
    setLoading(true)
    setError('')
    api
      .toolSearchStatus()
      .then(setStatus)
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => load(), [load])

  async function toggle() {
    if (!status || saving || status.available === false || status.supported === false) return
    setSaving(true)
    setError('')
    try {
      const saved = await api.toolSearchConfig({ enabled: !status.enabled })
      setStatus((current) => (current ? { ...current, ...saved } : saved))
      try {
        const refreshed = await api.toolSearchStatus()
        setStatus({ ...refreshed, refresh_warning: saved.refresh_warning })
      } catch {
        // The mutation already committed. Keep the new value visible and make
        // only the follow-up status refresh a warning, so retrying cannot
        // accidentally toggle the setting a second time.
        setStatus((current) =>
          current ? { ...current, refresh_warning: t('settings.general.toolSearchStatusRefreshFailed') } : current,
        )
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  const unavailable = status?.available === false || status?.supported === false
  const warning = status?.refresh_warning || status?.warning

  return (
    <div className="space-y-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3.5">
      <div className="flex items-center gap-2">
        <ServerStackIcon className="h-4 w-4 text-[var(--color-muted-foreground)]" />
        <h4 className="text-[12px] font-semibold text-[var(--color-foreground)]">
          {t('settings.general.toolSearchTitle')}
        </h4>
        {status && !unavailable && (
          <span className={status.enabled ? CHIP_ACCENT : CHIP}>
            {t(status.enabled ? 'settings.general.toolSearchModeProgressive' : 'settings.general.toolSearchModeEager')}
          </span>
        )}
      </div>
      <p className="text-[11px] leading-relaxed text-[var(--color-muted-foreground)]">
        {t('settings.general.toolSearchDesc')}
      </p>

      <div className={ROW}>
        <div className="min-w-0 flex-1">
          <div className="text-[12px] font-medium text-[var(--color-foreground)]">
            {t('settings.general.toolSearchToggle')}
          </div>
          <div className="text-[11px] text-[var(--color-muted-foreground)]">
            {unavailable ? t('settings.general.toolSearchUnavailable') : t('settings.general.toolSearchToggleDesc')}
          </div>
        </div>
        <Switch
          on={status?.enabled ?? false}
          onClick={() => void toggle()}
          ariaLabel={t('settings.general.toolSearchToggle')}
          disabled={loading || saving || !status || unavailable}
        />
      </div>

      {status && !unavailable && (
        <div className="grid grid-cols-3 gap-2">
          {([
            ['direct_count', 'toolSearchDirect'],
            ['deferred_count', 'toolSearchDeferred'],
            ['mcp_deferred_count', 'toolSearchMCP'],
          ] as const).map(([field, label]) => (
            <div key={field} className="rounded-[var(--radius-md)] bg-[var(--color-muted)] px-3 py-2.5">
              <div className="font-mono text-[15px] font-semibold text-[var(--color-foreground)]">
                {status[field] ?? '—'}
              </div>
              <div className="text-[10px] text-[var(--color-muted-foreground)]">
                {t(`settings.general.${label}`)}
              </div>
            </div>
          ))}
        </div>
      )}

      {warning && (
        <div role="status" aria-live="polite" className="text-[11px] text-[var(--color-warning-fg)]">
          {warning}
        </div>
      )}
      {error && (
        <div role="alert" aria-live="assertive" className="flex items-center justify-between gap-3 text-[11px] text-[var(--color-destructive)]">
          <span>{t('settings.general.toolSearchFailed', { reason: error })}</span>
          <button type="button" className={`${BTN_SECONDARY} ${BTN_XS}`} onClick={load}>
            {t('common.retry')}
          </button>
        </div>
      )}
    </div>
  )
}

function ApprovalReviewSection() {
  const { t } = useTranslation()
  const dispatch = useAppDispatch()
  const [cfg, setCfg] = useState<ApprovalReviewConfig>({})
  const [defaults, setDefaults] = useState<ApprovalReviewDefaults | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [loadError, setLoadError] = useState('')
  const [saved, setSaved] = useState(false)

  // The reviewer model picker offers the same enabled-model list as the chat
  // picker and the small_model role picker, so a model is chosen the same way
  // everywhere. This section lives in the Review tab, which does not load the
  // list itself — refresh it here rather than depend on the Providers tab
  // having been opened first.
  const pickerProviders = useAppSelector((s) => s.model.providers)

  // Nothing may be saved until a load succeeds: cfg starts empty and the POST
  // replaces the whole approval_review block, so saving an unloaded form would
  // wipe the stored model/policy/timeout.
  const load = useCallback(() => {
    setLoading(true)
    setLoadError('')
    api
      .approvalReviewConfig()
      .then(({ defaults: d, ...stored }) => {
        setCfg(stored)
        setDefaults(d ?? null)
      })
      .catch((err) => setLoadError(err instanceof Error ? err.message : String(err)))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => {
    load()
    void dispatch(loadModels())
  }, [load, dispatch])

  async function save() {
    setSaving(true)
    setError('')
    setSaved(false)
    try {
      await api.setApprovalReviewConfig(cfg)
      setSaved(true)
      window.setTimeout(() => setSaved(false), 2000)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  function update(partial: Partial<ApprovalReviewConfig>) {
    setCfg((prev) => ({ ...prev, ...partial }))
  }

  // Enabled models grouped by provider, matching the small_model role picker.
  const modelOptions = pickerProviders
    .map((p) => ({
      ...p,
      models: p.models.filter((m) =>
        m.enabled && m.tool_call && (m.output_modalities?.includes('text') ?? true),
      ),
    }))
    .filter((p) => p.models.length > 0)
  const storedModel = cfg.model ?? ''
  // '' and the 'small' alias resolve identically — review.resolveModelRef falls
  // through to small_model → main model for both — so they share one option
  // instead of asking the user to choose between two spellings of the same
  // thing. A config that already says 'small' selects it and keeps that value
  // until the user actually picks something else.
  const followsSmallModel = storedModel === '' || storedModel === 'small'
  const selectedModel = followsSmallModel ? '' : storedModel
  // A concrete ref whose model was since disabled or removed still renders
  // (marked unavailable) so it can be seen and cleared rather than silently
  // snapping to another option.
  const reviewerModelListed =
    followsSmallModel || modelOptions.some((p) => p.models.some((m) => `${p.id}/${m.id}` === storedModel))

  if (loading) {
    return (
      <div className="space-y-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3.5">
        <div className="animate-pulse py-4 text-center text-xs text-[var(--color-muted-foreground)]">
          {t('settings.general.approvalReviewLoading')}
        </div>
      </div>
    )
  }

  if (loadError) {
    return (
      <div className="space-y-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3.5">
        <div className="flex items-center gap-2">
          <ShieldCheckIcon className="h-4 w-4 text-[var(--color-muted-foreground)]" />
          <h4 className="text-[12px] font-semibold text-[var(--color-foreground)]">{t('settings.general.approvalReviewTitle')}</h4>
        </div>
        <div className="text-[11px] text-[var(--color-destructive)]">
          {t('settings.general.approvalReviewLoadFailed', { reason: loadError })}
        </div>
        <div className="flex justify-end">
          <button type="button" onClick={load} className={`${BTN_SECONDARY} ${BTN_SM}`}>
            {t('common.retry')}
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3.5">
      <div className="flex items-center gap-2">
        <ShieldCheckIcon className="h-4 w-4 text-[var(--color-muted-foreground)]" />
        <h4 className="text-[12px] font-semibold text-[var(--color-foreground)]">{t('settings.general.approvalReviewTitle')}</h4>
      </div>
      <p className="text-[11px] leading-relaxed text-[var(--color-muted-foreground)]">{t('settings.general.approvalReviewDesc')}</p>

      <Field label={t('settings.general.approvalReviewModel')}>
        <select
          value={selectedModel}
          onChange={(e) => update({ model: e.target.value })}
          className={INPUT_SM}
        >
          <option value="">{t('settings.general.approvalReviewModelUnset')}</option>
          {!reviewerModelListed && (
            <option value={storedModel}>
              {storedModel} — {t('settings.general.approvalReviewModelUnavailable')}
            </option>
          )}
          {modelOptions.map((p) => (
            <optgroup key={p.id} label={p.name || p.id}>
              {p.models.map((m) => (
                <option key={m.id} value={`${p.id}/${m.id}`}>
                  {m.name || m.id}
                </option>
              ))}
            </optgroup>
          ))}
        </select>
      </Field>

      <Field label={t('settings.general.approvalReviewPolicy')}>
        <textarea
          value={cfg.policy ?? ''}
          onChange={(e) => update({ policy: e.target.value })}
          placeholder={t('settings.general.approvalReviewPolicyPlaceholder')}
          className={TEXTAREA}
        />
      </Field>

      {/* Both fields keep "empty = follow the built-in default"; the resolved
          default is shown as the placeholder so it stays visible without being
          frozen into the config on save. */}
      <div className="grid grid-cols-2 gap-3">
        <Field label={t('settings.general.approvalReviewTimeout')}>
          <input
            type="number"
            min={0}
            value={cfg.timeout_seconds ? cfg.timeout_seconds : ''}
            onChange={(e) => update({ timeout_seconds: e.target.value === '' ? 0 : parseInt(e.target.value, 10) })}
            placeholder={defaults ? String(defaults.timeout_seconds) : ''}
            className={INPUT}
          />
        </Field>
        <Field label={t('settings.general.approvalReviewAuditPath')}>
          <input
            type="text"
            value={cfg.audit_path ?? ''}
            onChange={(e) => update({ audit_path: e.target.value })}
            placeholder={defaults?.audit_path ?? ''}
            className={INPUT_MONO}
          />
        </Field>
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div className={ROW}>
          <div className="min-w-0 flex-1">
            <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.general.approvalReviewInvestigate')}</div>
            <div className="text-[11px] text-[var(--color-muted-foreground)]">{t('settings.general.approvalReviewInvestigateDesc')}</div>
          </div>
          <Switch on={!!cfg.investigate} onClick={() => update({ investigate: !cfg.investigate })} />
        </div>
        <div className={ROW}>
          <div className="min-w-0 flex-1">
            <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.general.approvalReviewReuseSession')}</div>
            <div className="text-[11px] text-[var(--color-muted-foreground)]">{t('settings.general.approvalReviewReuseSessionDesc')}</div>
          </div>
          <Switch on={!!cfg.reuse_session} onClick={() => update({ reuse_session: !cfg.reuse_session })} />
        </div>
      </div>

      {error && (
        <div className="text-[11px] text-[var(--color-destructive)]">{t('settings.general.approvalReviewSaveFailed', { reason: error })}</div>
      )}
      {saved && <div className="text-[11px] text-[var(--color-success)]">{t('settings.general.approvalReviewSaved')}</div>}

      <div className="flex justify-end">
        <button type="button" disabled={saving} onClick={() => void save()} className={`${BTN_PRIMARY} ${BTN_SM}`}>
          {saving ? t('common.loading') : t('common.save')}
        </button>
      </div>
    </div>
  )
}

// ════════════════════════════════════════════════════════════════════════════
// Developer tab — logging + tracing (Langfuse) toggles.
// Both switches take effect on the next app start; the running process keeps
// its current logger / tracer. Mirrors the BLE toggle's "restart required"
// semantics in GeneralTab.
// ════════════════════════════════════════════════════════════════════════════

function DeveloperTab() {
  const { t } = useTranslation()
  const [status, setStatus] = useState<DevOptionsStatusResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [savingField, setSavingField] = useState<'' | 'logging' | 'tracing'>('')
  const [error, setError] = useState('')

  const load = useCallback(() => {
    setLoading(true)
    setError('')
    api
      .devOptionsStatus()
      .then(setStatus)
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => load(), [load])

  async function toggle(field: 'logging' | 'tracing') {
    if (!status || savingField || status.available === false) return
    setSavingField(field)
    setError('')
    try {
      const next = field === 'logging' ? !status.logging_enabled : !status.tracing_enabled
      const saved = await api.devOptionsConfig(
        field === 'logging' ? { logging_enabled: next } : { tracing_enabled: next },
      )
      setStatus((current) =>
        current
          ? {
              ...current,
              logging_enabled: saved.logging_enabled,
              tracing_enabled: saved.tracing_enabled,
            }
          : current,
      )
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSavingField('')
    }
  }

  const unavailable = status?.available === false
  const langfuseConfigured = !!status?.langfuse_configured

  return (
    <div className="space-y-5">
      <h3 className={SECTION_TITLE}>{t('settings.tabs.developer')}</h3>

      {/* Logging */}
      <div className="space-y-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3.5">
        <div className="flex items-center gap-2">
          <CommandLineIcon className="h-4 w-4 text-[var(--color-muted-foreground)]" />
          <h4 className="text-[12px] font-semibold text-[var(--color-foreground)]">
            {t('settings.developer.loggingTitle')}
          </h4>
        </div>
        <p className="text-[11px] leading-relaxed text-[var(--color-muted-foreground)]">
          {t('settings.developer.loggingDesc')}
        </p>
        <div className={ROW}>
          <div className="min-w-0 flex-1">
            <div className="text-[12px] font-medium text-[var(--color-foreground)]">
              {t('settings.developer.loggingToggle')}
            </div>
            <div className="text-[11px] text-[var(--color-muted-foreground)]">
              {t('settings.developer.loggingToggleDesc')}
            </div>
          </div>
          <Switch
            on={status?.logging_enabled ?? true}
            onClick={() => void toggle('logging')}
            ariaLabel={t('settings.developer.loggingToggle')}
            disabled={loading || savingField !== '' || !status || unavailable}
          />
        </div>
      </div>

      {/* Tracing */}
      <div className="space-y-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3.5">
        <div className="flex items-center gap-2">
          <BoltIcon className="h-4 w-4 text-[var(--color-muted-foreground)]" />
          <h4 className="text-[12px] font-semibold text-[var(--color-foreground)]">
            {t('settings.developer.tracingTitle')}
          </h4>
        </div>
        <p className="text-[11px] leading-relaxed text-[var(--color-muted-foreground)]">
          {t('settings.developer.tracingDesc')}
        </p>
        <div className={ROW}>
          <div className="min-w-0 flex-1">
            <div className="text-[12px] font-medium text-[var(--color-foreground)]">
              {t('settings.developer.tracingToggle')}
            </div>
            <div className="text-[11px] text-[var(--color-muted-foreground)]">
              {unavailable
                ? t('settings.developer.tracingUnavailable')
                : !langfuseConfigured && status
                  ? t('settings.developer.tracingNotConfigured')
                  : t('settings.developer.tracingToggleDesc')}
            </div>
          </div>
          <Switch
            on={status?.tracing_enabled ?? true}
            onClick={() => void toggle('tracing')}
            ariaLabel={t('settings.developer.tracingToggle')}
            disabled={loading || savingField !== '' || !status || unavailable}
          />
        </div>
      </div>

      {/* Langfuse credentials */}
      <LangfuseConfigCard
        status={status}
        disabled={loading || savingField !== '' || unavailable}
        onChanged={load}
      />

      {error && (
        <div
          role="alert"
          aria-live="assertive"
          className="flex items-center justify-between gap-3 text-[11px] text-[var(--color-destructive)]"
        >
          <span>{t('settings.developer.failed', { reason: error })}</span>
          <button type="button" className={`${BTN_SECONDARY} ${BTN_XS}`} onClick={load}>
            {t('common.retry')}
          </button>
        </div>
      )}
    </div>
  )
}

// LangfuseConfigCard renders the credentials form for the Langfuse tracer.
// Mirrors the ProviderForm secret-input discipline: host is seeded from the
// stored value (non-secret), public_key/secret_key start blank on edit and
// are only sent when the user types something — the backend keeps the prior
// value when they are empty. A dedicated "Clear" button wipes the block.
function LangfuseConfigCard({
  status,
  disabled,
  onChanged,
}: {
  status: DevOptionsStatusResponse | null
  disabled: boolean
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const lf = status?.langfuse
  const configured = !!status?.langfuse_configured

  const [host, setHost] = useState('')
  const [publicKey, setPublicKey] = useState('')
  const [secretKey, setSecretKey] = useState('')
  const [saving, setSaving] = useState(false)
  const [clearing, setClearing] = useState(false)
  const [error, setError] = useState('')
  const [confirmClear, setConfirmClear] = useState(false)
  const [seeded, setSeeded] = useState(false)

  // Seed host from the loaded status (non-secret). The secret inputs stay
  // blank and only show a placeholder hinting at the stored (masked) value.
  useEffect(() => {
    if (lf && !seeded) {
      setHost(lf.host ?? '')
      setSeeded(true)
    }
  }, [lf, seeded])

  async function save(e: React.FormEvent) {
    e.preventDefault()
    if (saving || clearing || disabled) return
    // require both keys when nothing is configured yet; allow partial edits otherwise
    if (!configured && (!publicKey.trim() || !secretKey.trim())) {
      setError(t('settings.developer.langfuseBothKeysRequired'))
      return
    }
    setSaving(true)
    setError('')
    try {
      const payload: Parameters<typeof api.devOptionsConfig>[0] = {
        langfuse: {
          host: host.trim(),
          public_key: publicKey.trim(),
          secret_key: secretKey.trim(),
        },
      }
      await api.devOptionsConfig(payload)
      setPublicKey('')
      setSecretKey('')
      setSeeded(false)
      onChanged()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  async function clear() {
    if (clearing || !configured) return
    setClearing(true)
    setError('')
    try {
      await api.devOptionsConfig({ langfuse_clear: true })
      setHost('')
      setPublicKey('')
      setSecretKey('')
      setSeeded(false)
      setConfirmClear(false)
      onChanged()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setClearing(false)
    }
  }

  const busy = saving || clearing || disabled

  return (
    <form
      onSubmit={save}
      className="space-y-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3.5"
    >
      <div className="flex items-center gap-2">
        <KeyIcon className="h-4 w-4 text-[var(--color-muted-foreground)]" />
        <h4 className="text-[12px] font-semibold text-[var(--color-foreground)]">
          {t('settings.developer.langfuseTitle')}
        </h4>
        {configured && <span className={CHIP_ACCENT}>{t('settings.developer.langfuseConfiguredBadge')}</span>}
      </div>
      <p className="text-[11px] leading-relaxed text-[var(--color-muted-foreground)]">
        {t('settings.developer.langfuseDesc')}
      </p>

      <Field label={t('settings.developer.langfuseHost')}>
        <input
          value={host}
          onChange={(e) => setHost(e.target.value)}
          type="text"
          autoComplete="url"
          spellCheck={false}
          placeholder={lf?.default_host || 'https://cloud.langfuse.com'}
          className={INPUT_MONO}
        />
      </Field>

      <Field label={t('settings.developer.langfusePublicKey')}>
        <input
          value={publicKey}
          onChange={(e) => setPublicKey(e.target.value)}
          type="password"
          autoComplete="off"
          spellCheck={false}
          placeholder={
            configured && lf?.public_key_set
              ? `${lf?.public_key ?? '••••'} — ${t('settings.developer.langfuseUnchanged')}`
              : 'pk-lf-…'
          }
          className={INPUT_MONO}
        />
      </Field>

      <Field label={t('settings.developer.langfuseSecretKey')}>
        <input
          value={secretKey}
          onChange={(e) => setSecretKey(e.target.value)}
          type="password"
          autoComplete="off"
          spellCheck={false}
          placeholder={
            configured && lf?.secret_key_set
              ? `${t('settings.developer.langfuseUnchanged')}`
              : 'sk-lf-…'
          }
          className={INPUT_MONO}
        />
      </Field>

      {error && (
        <div role="alert" aria-live="assertive" className="text-[11px] text-[var(--color-destructive)]">
          {t('settings.developer.failed', { reason: error })}
        </div>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-[11px] text-[var(--color-muted-foreground)]">
          {t('settings.developer.langfuseRestartHint')}
        </div>
        <div className="flex items-center gap-2">
          {configured &&
            (confirmClear ? (
              <>
                <button
                  type="button"
                  className={`${BTN_GHOST} ${BTN_XS}`}
                  disabled={busy}
                  onClick={() => setConfirmClear(false)}
                >
                  {t('common.cancel')}
                </button>
                <button
                  type="button"
                  className={`${BTN_DANGER} ${BTN_XS}`}
                  disabled={busy}
                  onClick={() => void clear()}
                >
                  {clearing ? t('common.loading') : t('settings.developer.langfuseClearConfirm')}
                </button>
              </>
            ) : (
              <button
                type="button"
                className={`${BTN_GHOST} ${BTN_XS}`}
                disabled={busy}
                onClick={() => setConfirmClear(true)}
              >
                {t('settings.developer.langfuseClear')}
              </button>
            ))}
          <button type="submit" className={`${BTN_PRIMARY} ${BTN_XS}`} disabled={busy}>
            {saving ? t('common.loading') : t('common.save')}
          </button>
        </div>
      </div>
    </form>
  )
}

// ════════════════════════════════════════════════════════════════════════════
// Shortcuts tab — static keyboard shortcuts reference
// ════════════════════════════════════════════════════════════════════════════

const SHORTCUTS: { keys: string; labelKey: string }[] = [
  { keys: '⌘K', labelKey: 'commandPalette' },
  { keys: '⇧⌘O', labelKey: 'newChat' },
  { keys: '⌘,', labelKey: 'openSettings' },
  { keys: '⇧⌘P', labelKey: 'planMode' },
  { keys: '⇧⌘E', labelKey: 'filesPanel' },
  { keys: '⇧⌘G', labelKey: 'changesPanel' },
  { keys: '⌘J', labelKey: 'toggleTerminal' },
  { keys: '⌘L', labelKey: 'focusInput' },
]

function ShortcutsTab() {
  const { t } = useTranslation()
  return (
    <div className="space-y-4">
      <h3 className={SECTION_TITLE}>{t('settings.shortcuts.title')}</h3>
      <div className="overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)]">
        {SHORTCUTS.map((s, i) => (
          <div
            key={s.keys}
            className="flex items-center justify-between px-3.5 py-2.5"
            style={{ borderTop: i === 0 ? 'none' : '1px solid var(--color-border)' }}
          >
            <span className="text-[12px] text-[var(--color-foreground)]">
              {t(`settings.shortcuts.items.${s.labelKey}`)}
            </span>
            <kbd
              className="inline-flex h-6 min-w-[28px] items-center justify-center rounded-[var(--radius-sm)] border border-[var(--color-border)] bg-[var(--color-muted)] px-2 font-mono text-[11px] text-[var(--color-foreground)]"
            >
              {s.keys}
            </kbd>
          </div>
        ))}
      </div>
      <div className="text-[10.5px] leading-relaxed text-[var(--color-muted-foreground)]">
        {t('settings.shortcuts.windowsHint')}
      </div>
    </div>
  )
}

// ════════════════════════════════════════════════════════════════════════════
// MCP tab — servers CRUD (local/http/sse) + OAuth login polling
// ════════════════════════════════════════════════════════════════════════════

interface MCPHeaderRow {
  key: string
  value: string
}
export interface MCPForm {
  name: string
  transport: 'local' | 'http' | 'sse'
  url: string
  command: string
  argsText: string
  headers: MCPHeaderRow[]
  timeout: string
  oauthEnabled: boolean
  clientId: string
  clientSecret: string
  scopesText: string
  originalHeaderKeys: string[]
  oauthConfigured: boolean
  clientSecretPresent: boolean
  removeOAuth: boolean
  removeClientSecret: boolean
}

export function emptyMCPForm(): MCPForm {
  return {
    name: '',
    transport: 'http',
    url: '',
    command: '',
    argsText: '',
    headers: [],
    timeout: '',
    oauthEnabled: false,
    clientId: '',
    clientSecret: '',
    scopesText: '',
    originalHeaderKeys: [],
    oauthConfigured: false,
    clientSecretPresent: false,
    removeOAuth: false,
    removeClientSecret: false,
  }
}

/** Build the explicit MCP secret-mutation contract. Empty/masked values mean
 * keep on the server, so deletion must travel through dedicated fields. */
export function buildMCPRequest(form: MCPForm, editing: boolean): MCPServerRequest {
  // An empty value on an existing row is a deletion gesture. Do not send that
  // ambiguous empty value alongside remove_headers: the backend intentionally
  // interprets empty/masked secret values as "keep" for safety.
  const hdrs = buildHeaders(form.headers.filter((header) => header.value !== ''))
  const activeHeaderKeys = new Set(
    form.headers
      .filter((header) => header.key.trim() && header.value !== '')
      .map((header) => header.key.trim().toLowerCase()),
  )
  const removedHeaders = editing
    ? form.originalHeaderKeys.filter((key) => !activeHeaderKeys.has(key.trim().toLowerCase()))
    : []
  const req: MCPServerRequest = { name: form.name.trim(), type: form.transport }

  if (form.transport === 'local') {
    req.command = form.command.trim()
    req.args = form.argsText.trim() ? form.argsText.trim().split(/\s+/) : undefined
    if (editing && form.originalHeaderKeys.length > 0) req.remove_headers = form.originalHeaderKeys
    if (editing && form.oauthConfigured) req.remove_oauth = true
    return req
  }

  req.url = form.url.trim()
  if (Object.keys(hdrs).length) req.headers = hdrs
  if (removedHeaders.length > 0) req.remove_headers = removedHeaders
  if (form.timeout.trim()) req.timeout = Number(form.timeout)
  if (editing && form.removeOAuth) {
    req.remove_oauth = true
  } else if (form.oauthEnabled || form.oauthConfigured || form.clientId.trim()) {
    req.oauth = {
      enabled: form.oauthEnabled,
      client_id: form.clientId.trim() || undefined,
      client_secret: form.clientSecret.trim() || undefined,
      scopes: form.scopesText.trim() ? form.scopesText.trim().split(/\s+/) : undefined,
      remove_client_secret: form.removeClientSecret || undefined,
    }
  }
  return req
}

function MCPTab() {
  const { t } = useTranslation()
  const [servers, setServers] = useState<Record<string, MCPServerInfo>>({})
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<string | null>(null) // null=list, ''=add, name=edit
  const [form, setForm] = useState<MCPForm>(emptyMCPForm())
  const [saving, setSaving] = useState(false)
  const [formError, setFormError] = useState('')
  const [loginBusy, setLoginBusy] = useState('')
  const [loginMsg, setLoginMsg] = useState('')
  const [loginMsgFor, setLoginMsgFor] = useState('')
  const pollRef = useRef<number | null>(null)

  async function load() {
    setLoading(true)
    try {
      const r = await api.mcpList()
      setServers(r.servers)
    } catch {
      /* ignore */
    }
    setLoading(false)
  }

  useEffect(() => {
    void load()
    return () => stopPoll()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function stopPoll() {
    if (pollRef.current) {
      window.clearInterval(pollRef.current)
      pollRef.current = null
    }
  }

  function openAdd() {
    setForm(emptyMCPForm())
    setFormError('')
    setEditing('')
  }

  function openEdit(info: MCPServerInfo) {
    const oauth = info.oauth_config
    setForm({
      name: info.name,
      transport: info.type === 'stdio' || info.type === '' ? 'local' : (info.type as 'http' | 'sse'),
      url: info.url ?? '',
      command: info.command ?? '',
      argsText: (info.args ?? []).join(' '),
      headers: Object.entries(info.headers ?? {}).map(([key, value]) => ({ key, value })),
      timeout: info.timeout ? String(info.timeout) : '',
      oauthEnabled: oauth?.enabled ?? info.oauth,
      clientId: oauth?.client_id ?? '',
      clientSecret: '',
      scopesText: (oauth?.scopes ?? []).join(' '),
      originalHeaderKeys: Object.keys(info.headers ?? {}),
      oauthConfigured: !!oauth,
      clientSecretPresent: !!oauth?.client_secret,
      removeOAuth: false,
      removeClientSecret: false,
    })
    setFormError('')
    setEditing(info.name)
  }

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setFormError('')
    if (!form.name.trim()) {
      setFormError(t('settings.mcp.serverName') + ' *')
      return
    }
    if (form.transport === 'local' && !form.command.trim()) {
      setFormError(t('settings.mcp.command') + ' *')
      return
    }
    if (form.transport !== 'local' && !form.url.trim()) {
      setFormError(t('settings.mcp.url') + ' *')
      return
    }
    setSaving(true)
    try {
      const req = buildMCPRequest(form, !!editing)
      if (editing) await api.mcpUpdate(editing, req)
      else await api.mcpCreate(req)
      setEditing(null)
      await load()
    } catch (err) {
      setFormError(err instanceof Error ? err.message : t('settings.mcp.status.error'))
    }
    setSaving(false)
  }

  async function remove(name: string) {
    try {
      await api.mcpDelete(name)
      await load()
    } catch (err) {
      console.error('Failed to delete MCP server:', err)
    }
  }

  async function toggle(name: string, enabled: boolean) {
    try {
      await api.mcpToggle(name, enabled)
      await load()
    } catch (err) {
      console.error('Failed to toggle MCP server:', err)
    }
  }

  async function login(name: string) {
    setLoginBusy(name)
    setLoginMsgFor(name)
    setLoginMsg(t('settings.mcp.waitingBrowser'))
    try {
      await api.mcpLogin(name)
    } catch (err) {
      setLoginMsg(err instanceof Error ? err.message : t('settings.mcp.status.error'))
      setLoginBusy('')
      return
    }
    stopPoll()
    pollRef.current = window.setInterval(async () => {
      try {
        const st = await api.mcpLoginStatus(name)
        if (st.status === 'authorized') {
          stopPoll()
          setLoginBusy('')
          setLoginMsg('')
          setLoginMsgFor('')
          await load()
        } else if (st.status === 'error') {
          stopPoll()
          setLoginBusy('')
          setLoginMsgFor(name)
          setLoginMsg(st.message || t('settings.mcp.status.error'))
        } else if (st.status === 'needs_client_id') {
          stopPoll()
          setLoginBusy('')
          setLoginMsgFor(name)
          setLoginMsg('This server does not support automatic registration. Edit it and set an OAuth Client ID, then log in again.')
        }
      } catch {
        /* keep polling */
      }
    }, 1500)
  }

  const serverList = Object.entries(servers)

  // Add/edit form view
  if (editing !== null) {
    return (
      <form onSubmit={save}>
        <div className="mb-4 flex items-center justify-between">
          <h3 className={SECTION_TITLE}>{editing ? t('settings.mcp.editServer') : t('settings.mcp.addServer')}</h3>
          <button type="button" className={`${BTN_GHOST} ${BTN_SM}`} onClick={() => setEditing(null)}>
            ← {t('common.back')}
          </button>
        </div>

        <div className="overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)]">
          <div className="border-b border-[var(--color-border)] bg-[var(--color-muted)] px-3.5 py-2.5 text-[11px] font-semibold uppercase tracking-wide text-[var(--color-muted-foreground)]">
            {editing ? t('settings.mcp.editServer') : t('settings.mcp.addServer')}
          </div>
          <div className="p-4">
            <Field label={t('settings.mcp.serverName')}>
              <input
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
                disabled={!!editing}
                type="text"
                placeholder="my-server"
                className={INPUT}
              />
            </Field>

            <Field label={t('settings.mcp.transport')}>
              <Segmented
                value={form.transport}
                onChange={(t) => setForm((f) => ({ ...f, transport: t }))}
                options={[
                  { value: 'local', label: t('settings.mcp.transportLocal') },
                  { value: 'http', label: 'HTTP' },
                  { value: 'sse', label: 'SSE' },
                ]}
              />
            </Field>

            {form.transport !== 'local' ? (
              <>
                <Field label={t('settings.mcp.url')}>
                  <input
                    value={form.url}
                    onChange={(e) => setForm((f) => ({ ...f, url: e.target.value }))}
                    type="text"
                    placeholder="https://api.example.com/mcp"
                    className={INPUT_MONO}
                  />
                </Field>

                <div className="mb-3.5">
                  <div className="mb-1.5 flex items-center justify-between">
                    <label className={LABEL + ' !mb-0'}>{t('settings.providers.headers')}</label>
                    <button
                      type="button"
                      className={`${BTN_GHOST} ${BTN_XS}`}
                      onClick={() => setForm((f) => ({ ...f, headers: [...f.headers, { key: '', value: '' }] }))}
                    >
                      + {t('settings.providers.addHeader')}
                    </button>
                  </div>
                  {form.headers.map((h, i) => (
                    <div key={i} className="mb-2 flex gap-2">
                      <input
                        value={h.key}
                        onChange={(e) =>
                          setForm((f) => ({
                            ...f,
                            headers: f.headers.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)),
                          }))
                        }
                        type="text"
                        placeholder={t('settings.providers.headerKey')}
                        className={INPUT_MONO}
                      />
                      <input
                        value={h.value}
                        onChange={(e) =>
                          setForm((f) => ({
                            ...f,
                            headers: f.headers.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)),
                          }))
                        }
                        type="text"
                        placeholder="value"
                        className={INPUT_MONO}
                      />
                      <button
                        type="button"
                        onClick={() => setForm((f) => ({ ...f, headers: f.headers.filter((_, j) => j !== i) }))}
                        aria-label={`Remove header ${h.key || i + 1}`}
                        className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-sm)] border border-[var(--color-border)] text-[var(--color-muted-foreground)] hover:bg-[var(--color-secondary)]"
                      >
                        <TrashIcon className="h-3.5 w-3.5" />
                      </button>
                    </div>
                  ))}
                </div>

                <div className="mb-3.5 flex items-center justify-between rounded-[var(--radius-md)] border border-[var(--color-border)] bg-[var(--color-muted)] px-3 py-2">
                  <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.mcp.useOauth')}</div>
                  <Switch
                    on={form.oauthEnabled}
                    ariaLabel={t('settings.mcp.useOauth')}
                    onClick={() => setForm((f) => f.oauthEnabled
                      ? { ...f, oauthEnabled: false, removeOAuth: f.oauthConfigured }
                      : { ...f, oauthEnabled: true, removeOAuth: false })}
                  />
                </div>

                {form.removeOAuth && (
                  <div role="status" className="mb-3.5 rounded-[var(--radius-md)] border border-[var(--color-warning-fg)] bg-[var(--color-warning-bg)] px-3 py-2 text-[11px] text-[var(--color-warning-fg)]">
                    OAuth configuration and its saved client secret will be removed when you save.
                  </div>
                )}

                {form.oauthEnabled && (
                  <div className="mb-3.5 space-y-3">
                    <Field label={t('settings.mcp.oauthClientId')}>
                      <input
                        value={form.clientId}
                        onChange={(e) => setForm((f) => ({ ...f, clientId: e.target.value }))}
                        type="text"
                        placeholder="Optional — leave blank to auto-register"
                        className={INPUT_MONO}
                      />
                    </Field>
                    <Field label={t('settings.mcp.oauthClientSecret')}>
                      <input
                        value={form.clientSecret}
                        onChange={(e) => setForm((f) => ({ ...f, clientSecret: e.target.value, removeClientSecret: false }))}
                        type="password"
                        disabled={form.removeClientSecret}
                        placeholder={form.clientSecretPresent
                          ? 'Saved secret — leave blank to keep'
                          : 'Optional (confidential clients)'}
                        className={INPUT_MONO}
                      />
                      {form.clientSecretPresent && (
                        <button
                          type="button"
                          className={`${BTN_GHOST} ${BTN_XS} mt-1.5`}
                          onClick={() => setForm((f) => ({
                            ...f,
                            clientSecret: '',
                            removeClientSecret: !f.removeClientSecret,
                          }))}
                        >
                          <TrashIcon className="h-3.5 w-3.5" />
                          {form.removeClientSecret ? 'Keep saved secret' : 'Clear saved secret'}
                        </button>
                      )}
                      {form.removeClientSecret && (
                        <div className="mt-1 text-[10.5px] text-[var(--color-warning-fg)]">
                          The saved client secret will be removed when you save.
                        </div>
                      )}
                    </Field>
                    <Field label={t('settings.mcp.oauthScopes')}>
                      <input
                        value={form.scopesText}
                        onChange={(e) => setForm((f) => ({ ...f, scopesText: e.target.value }))}
                        type="text"
                        placeholder="space-separated, optional"
                        className={INPUT_MONO}
                      />
                    </Field>
                  </div>
                )}

                <Field label={t('settings.mcp.timeout')}>
                  <input
                    value={form.timeout}
                    onChange={(e) => setForm((f) => ({ ...f, timeout: e.target.value }))}
                    type="number"
                    placeholder="180"
                    className={INPUT}
                    style={{ maxWidth: '120px' }}
                  />
                </Field>
              </>
            ) : (
              <>
                <Field label={t('settings.mcp.command')}>
                  <input
                    value={form.command}
                    onChange={(e) => setForm((f) => ({ ...f, command: e.target.value }))}
                    type="text"
                    placeholder="npx"
                    className={INPUT_MONO}
                  />
                </Field>
                <Field label={t('settings.mcp.arguments')}>
                  <input
                    value={form.argsText}
                    onChange={(e) => setForm((f) => ({ ...f, argsText: e.target.value }))}
                    type="text"
                    placeholder="-y @some/mcp-server"
                    className={INPUT_MONO}
                  />
                </Field>
              </>
            )}

            {formError && <div className="text-[11px] text-[var(--color-destructive)]">{formError}</div>}
          </div>
          <div className="flex justify-end gap-2 border-t border-[var(--color-border)] bg-[var(--color-muted)] px-3.5 py-3">
            <button type="button" className={BTN_SECONDARY} onClick={() => setEditing(null)}>
              {t('common.cancel')}
            </button>
            <button type="submit" disabled={saving} className={BTN_PRIMARY}>
              {saving ? t('settings.providers.saving') : t('settings.mcp.save')}
            </button>
          </div>
        </div>
      </form>
    )
  }

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <div className="flex items-baseline gap-2">
          <h3 className={SECTION_TITLE}>{t('settings.mcp.title')}</h3>
          <span className="font-mono text-[11px] text-[var(--color-muted-foreground)]">{serverList.length}</span>
        </div>
        <button type="button" className={`${BTN_SECONDARY} ${BTN_SM}`} onClick={openAdd}>
          <PlusIcon className="h-3.5 w-3.5" /> {t('settings.mcp.addServer')}
        </button>
      </div>

      {loading ? (
        <div className="animate-pulse py-6 text-center text-xs text-[var(--color-muted-foreground)]">{t('common.loading')}</div>
      ) : serverList.length === 0 ? (
        <EmptyState
          Icon={ServerStackIcon}
          title={t('settings.mcp.noneConfigured')}
          hint={t('settings.mcp.noneHint', { btn: t('settings.mcp.add') })}
        />
      ) : (
        <div className="space-y-2">
          {serverList.map(([name, info]) => {
            const Icon = info.type === 'sse' || info.type === 'http' ? GlobeAltIcon : BoltIcon
            return (
              <div
                key={name}
                className="flex flex-col rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3.5 py-2.5"
                data-muted={!info.enabled ? 'true' : 'false'}
              >
                <div className="flex w-full items-center gap-3">
                  <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
                    <Icon className="h-3.5 w-3.5" />
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2 text-[12px] font-medium text-[var(--color-foreground)]">
                      {name}
                      <span className={CHIP}>{info.type || 'stdio'}</span>
                      {info.scope === 'project' && <span className={CHIP_ACCENT}>{t('settings.scope.project')}</span>}
                      <span className="text-[10px]" style={{ color: mcpStatusColor(info) }}>
                        ● {mcpStatusLabel(info, t)}
                      </span>
                    </div>
                    <div className="truncate font-mono text-[11px] text-[var(--color-muted-foreground)]">
                      {info.type === 'sse' || info.type === 'http' ? info.url : info.command}
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1.5">
                    <button type="button" className={`${BTN_GHOST} ${BTN_XS}`} onClick={() => openEdit(info)}>
                      {t('common.edit')}
                    </button>
                    <button
                      type="button"
                      className={`${BTN_GHOST} ${BTN_XS} text-[var(--color-destructive)]`}
                      onClick={() => remove(name)}
                    >
                      {t('common.delete')}
                    </button>
                    <Switch on={info.enabled} onClick={() => toggle(name, !info.enabled)} />
                  </div>
                </div>
                {/* OAuth login row */}
                {(info.type === 'http' || info.type === 'sse') && (info.oauth || info.status === 'needs_auth') && (
                  <div className="mt-2 flex items-center gap-2 pl-10">
                    <button
                      type="button"
                      className={`${BTN_SECONDARY} ${BTN_XS}`}
                      disabled={loginBusy === name}
                      onClick={() => login(name)}
                    >
                      {loginBusy === name
                        ? t('settings.mcp.waitingBrowser')
                        : info.has_auth
                          ? t('settings.mcp.reauth')
                          : t('settings.mcp.login')}
                    </button>
                    {info.has_auth && <span className={CHIP + ' !bg-[var(--color-success-bg)] !text-[var(--color-success-fg)]'}>{t('settings.mcp.authenticated')}</span>}
                  </div>
                )}
                {loginBusy === name && loginMsg && (
                  <div className="mt-1 pl-10 text-[10px] text-[var(--color-muted-foreground)]">{loginMsg}</div>
                )}
                {loginMsgFor === name && loginMsg && !loginBusy && (
                  <div className="mt-1 pl-10 text-[10px] text-[var(--color-warning-fg)]">{loginMsg}</div>
                )}
                {info.error && (
                  <div className="mt-1 pl-10 font-mono text-[10px] text-[var(--color-error-fg)]">{info.error}</div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

// ════════════════════════════════════════════════════════════════════════════
// Skills tab — list + filter + search + enable/disable
// ════════════════════════════════════════════════════════════════════════════

function SkillsTab() {
  const { t } = useTranslation()
  const [skills, setSkills] = useState<SkillInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState<'all' | 'local' | 'builtin'>('all')
  const [search, setSearch] = useState('')

  async function load() {
    try {
      setSkills(await api.skillsList())
    } catch {
      /* ignore */
    }
    setLoading(false)
  }

  useEffect(() => {
    void load()
  }, [])

  async function toggle(name: string, enabled: boolean) {
    try {
      await api.skillToggle(name, enabled)
      setSkills((prev) => prev.map((s) => (s.name === name ? { ...s, enabled } : s)))
    } catch (err) {
      console.error('Failed to toggle skill:', err)
    }
  }

  const q = search.trim().toLowerCase()
  const filtered = skills.filter((s) => {
    if (filter === 'builtin' && !s.builtin) return false
    if (filter === 'local' && s.builtin) return false
    if (q && !s.name.toLowerCase().includes(q) && !(s.description ?? '').toLowerCase().includes(q)) return false
    return true
  })

  return (
    <div>
      <div className="mb-4 flex items-baseline gap-2">
        <h3 className={SECTION_TITLE}>{t('settings.skills.title')}</h3>
        <span className="font-mono text-[11px] text-[var(--color-muted-foreground)]">{skills.length}</span>
      </div>
      <div className="mb-3 flex items-center gap-2">
        <Segmented
          value={filter}
          onChange={(f) => setFilter(f)}
          options={[
            { value: 'all', label: t('settings.skills.filterAll') },
            { value: 'local', label: t('settings.skills.filterLocal') },
            { value: 'builtin', label: t('settings.skills.filterBuiltin') },
          ]}
        />
        <input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          type="text"
          placeholder={t('settings.skills.search')}
          className={INPUT}
          style={{ flex: 1 }}
        />
      </div>

      {loading ? (
        <div className="animate-pulse py-6 text-center text-xs text-[var(--color-muted-foreground)]">{t('settings.skills.loadingHint')}</div>
      ) : filtered.length === 0 ? (
        <EmptyState Icon={SparklesIcon} title={t('settings.skills.none')} hint={t('settings.skills.noneHint')} />
      ) : (
        <div className="space-y-2">
          {filtered.map((s) => (
            <div key={s.name} className={ROW} data-muted={!s.enabled ? 'true' : 'false'}>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 text-[12px] font-medium text-[var(--color-foreground)]">
                  {s.name}
                  {s.builtin && <span className={CHIP}>{t('settings.skills.builtin')}</span>}
                  {!s.builtin && s.source === 'project' && <span className={CHIP_ACCENT}>{t('settings.scope.project')}</span>}
                  {!s.builtin && s.source === 'agents' && <span className={CHIP}>{t('settings.scope.agents')}</span>}
                </div>
                {s.description && (
                  <div className="text-[11px] text-[var(--color-muted-foreground)]">{s.description}</div>
                )}
              </div>
              <Switch on={!!s.enabled} onClick={() => toggle(s.name, !s.enabled)} />
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// ════════════════════════════════════════════════════════════════════════════
// Memory tab — project memory, Dream consolidation, metadata and actions
// ════════════════════════════════════════════════════════════════════════════

const MEMORY_DEFAULTS: MemoryConfig = {
  enabled: true,
  generate: true,
  model: '',
  daily_token_budget: 300000,
  cooldown_hours: 6,
  max_age_days: 30,
  max_unused_days: 45,
  phase2_top_n: 40,
  summary_inject_tokens: 1200,
}

function formatBytes(value?: number): string {
  if (value == null || !Number.isFinite(value)) return '—'
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(value < 10240 ? 1 : 0)} KB`
  return `${(value / (1024 * 1024)).toFixed(1)} MB`
}

function MemoryTab() {
  const { t, i18n } = useTranslation()
  const [status, setStatus] = useState<MemoryStatusResponse | null>(null)
  const [cfg, setCfg] = useState<MemoryConfig>(MEMORY_DEFAULTS)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [action, setAction] = useState<'sync' | 'clear' | ''>('')
  const [advanced, setAdvanced] = useState(false)
  const [error, setError] = useState('')
  const [warning, setWarning] = useState('')
  const [message, setMessage] = useState('')
  const [pollError, setPollError] = useState('')
  const [pollingPaused, setPollingPaused] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const next = await api.memoryStatus()
      setStatus(next)
      setPollError('')
      setPollingPaused(false)
      setCfg({
        ...MEMORY_DEFAULTS,
        ...next.config,
        enabled: next.config?.enabled ?? next.enabled ?? MEMORY_DEFAULTS.enabled,
        generate: next.config?.generate ?? next.generate ?? MEMORY_DEFAULTS.generate,
        model: next.config?.model ?? next.model ?? '',
        daily_token_budget:
          next.config?.daily_token_budget ?? next.daily_token_budget ?? MEMORY_DEFAULTS.daily_token_budget,
      })
      setDirty(false)
      return true
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      return false
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  // Manual consolidation runs asynchronously. Refresh metadata without
  // replacing the draft form, then stop polling as soon as the run completes.
  useEffect(() => {
    if (!status?.running || pollingPaused) return
    const timer = window.setInterval(() => {
      void api
        .memoryStatus()
        .then(setStatus)
        .catch((err) => {
          const reason = err instanceof Error ? err.message : String(err)
          setPollError(t('settings.memory.pollFailed', { reason }))
          // Pause without claiming the server-side run stopped. Keeping the
          // last known busy state prevents duplicate sync/clear actions until a
          // successful status retry establishes the real terminal state.
          setPollingPaused(true)
        })
    }, 2000)
    return () => window.clearInterval(timer)
  }, [pollingPaused, status?.running, t])

  async function retryPoll() {
    try {
      const next = await api.memoryStatus()
      setStatus(next)
      setPollError('')
      setPollingPaused(false)
    } catch (err) {
      const reason = err instanceof Error ? err.message : String(err)
      setPollError(t('settings.memory.pollFailed', { reason }))
      setPollingPaused(true)
    }
  }

  function patch(next: Partial<MemoryConfig>) {
    setCfg((prev) => ({ ...prev, ...next }))
    setDirty(true)
    setWarning('')
    setMessage('')
  }

  function numberPatch(field: keyof MemoryConfig, raw: string) {
    const value = Number.parseInt(raw, 10)
    if (Number.isFinite(value)) patch({ [field]: value })
  }

  async function save() {
    setSaving(true)
    setError('')
    setWarning('')
    setMessage('')
    try {
      const response = await api.memoryConfig(cfg)
      setDirty(false)
      if (response.config) setCfg((current) => ({ ...current, ...response.config }))
      const refreshed = await load()
      if (!refreshed) {
        setError('')
        setWarning(t('settings.memory.refreshAfterAction'))
      } else if (response.warning || response.warning_code) {
        setWarning(response.warning || t('settings.memory.refreshWarning'))
      } else {
        setMessage(t('settings.memory.saved'))
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  async function syncNow() {
    setAction('sync')
    setError('')
    setWarning('')
    setMessage('')
    try {
      if (!status?.project) throw new Error(t('settings.memory.projectChanged'))
      const response = await api.memorySync(status.project)
      setMessage(response.message || t('settings.memory.syncStarted'))
      const refreshed = await load()
      if (!refreshed) {
        setError('')
        setWarning(t('settings.memory.refreshAfterAction'))
      } else if (response.warning || response.warning_code) {
        setWarning(response.warning || t('settings.memory.refreshWarning'))
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setAction('')
    }
  }

  async function clearProject() {
    if (!window.confirm(t('settings.memory.clearConfirm', { project: status?.project || '—' }))) return
    setAction('clear')
    setError('')
    setWarning('')
    setMessage('')
    try {
      if (!status?.project) throw new Error(t('settings.memory.projectChanged'))
      const response = await api.memoryClear(status.project)
      setMessage(response.message || t('settings.memory.cleared'))
      const refreshed = await load()
      if (!refreshed) {
        setError('')
        setWarning(t('settings.memory.refreshAfterAction'))
      } else if (response.warning || response.warning_code) {
        setWarning(response.warning || t('settings.memory.refreshWarning'))
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setAction('')
    }
  }

  const projectUnavailable = !status || status.supported === false || status.remote === true
  const configUnavailable = !status || status.available === false
  const busy = !!status?.running || !!status?.busy
  const controlsDisabled = loading || configUnavailable || saving || action !== ''
  const memoryActive = status?.config?.enabled ?? status?.enabled ?? false
  const generationActive =
    status?.effective_generate ??
    ((status?.config?.enabled ?? status?.enabled ?? false) && (status?.config?.generate ?? status?.generate ?? false))
  const todayTokens = status?.today_tokens ?? 0
  const budget = status?.config?.daily_token_budget ?? status?.daily_token_budget ?? cfg.daily_token_budget
  const budgetPct = budget > 0 ? Math.min(100, Math.round((todayTokens / budget) * 100)) : 0
  const notes = status?.notes_count ?? status?.inbox_count
  const summarySize = status?.summary_size ?? status?.summary_bytes
  const lastPipeline = status?.last_pipeline_at
  const lastConsolidation = status?.last_consolidation_at ?? status?.last_consolidation
  const initialLoadFailed = !status && !!error

  function formatTime(value?: string) {
    if (!value) return t('settings.memory.never')
    const date = new Date(value)
    if (Number.isNaN(date.getTime())) return value
    return new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
  }

  if (loading && !status) {
    return (
      <div className="animate-pulse py-12 text-center text-xs text-[var(--color-muted-foreground)]">
        {t('common.loading')}
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <div>
        <div className="flex items-center gap-2">
          <h3 className={SECTION_TITLE}>{t('settings.memory.title')}</h3>
          {busy && <span className={CHIP_ACCENT}>{t('settings.memory.running')}</span>}
        </div>
        <p className="mt-1 text-[11px] leading-relaxed text-[var(--color-muted-foreground)]">
          {t('settings.memory.subtitle')}
        </p>
      </div>

      {projectUnavailable && (
        <div className="flex items-start gap-2 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-muted)] p-3 text-[11px] text-[var(--color-muted-foreground)]">
          <ExclamationTriangleIcon className="mt-0.5 h-4 w-4 shrink-0" />
          <div className="min-w-0 flex-1">
            <div role={initialLoadFailed ? 'alert' : undefined} aria-live={initialLoadFailed ? 'assertive' : undefined}>
              {initialLoadFailed
                ? t('settings.memory.loadFailed', { reason: error })
                : status?.reason ||
                  status?.error ||
                  t(status?.remote ? 'settings.memory.remoteUnavailable' : 'settings.memory.unavailable')}
            </div>
            {initialLoadFailed && (
              <button type="button" className={`${BTN_SECONDARY} ${BTN_XS} mt-2`} onClick={() => void load()}>
                {t('common.retry')}
              </button>
            )}
          </div>
        </div>
      )}

      {status?.project && (
        <div className="rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3.5">
          <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.memory.currentProjectTitle')}</div>
          <div className="mt-0.5 text-[11px] text-[var(--color-muted-foreground)]">{t('settings.memory.currentProjectDesc')}</div>
          <div className="mt-1.5 truncate font-mono text-[10px] text-[var(--color-muted-foreground)]" title={status.project}>
            {status.project}
          </div>
        </div>
      )}

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <div className="rounded-[var(--radius-lg)] border border-[var(--color-border)] p-3">
          <div className="font-mono text-[15px] font-semibold text-[var(--color-foreground)]">{notes ?? '—'}</div>
          <div className="text-[10px] text-[var(--color-muted-foreground)]">{t('settings.memory.inboxNotes')}</div>
        </div>
        <div className="rounded-[var(--radius-lg)] border border-[var(--color-border)] p-3">
          <div className="font-mono text-[15px] font-semibold text-[var(--color-foreground)]">{formatBytes(summarySize)}</div>
          <div className="text-[10px] text-[var(--color-muted-foreground)]">{t('settings.memory.summary')}</div>
        </div>
        <div className="rounded-[var(--radius-lg)] border border-[var(--color-border)] p-3">
          <div className="font-mono text-[15px] font-semibold text-[var(--color-foreground)]">
            {status?.tracked_files ?? '—'}
          </div>
          <div className="text-[10px] text-[var(--color-muted-foreground)]">{t('settings.memory.trackedFiles')}</div>
        </div>
        <div className="rounded-[var(--radius-lg)] border border-[var(--color-border)] p-3">
          <div className="font-mono text-[15px] font-semibold text-[var(--color-foreground)]">
            {status?.extracted_count ?? '—'}
            {(status?.failed_count ?? 0) > 0 && (
              <span className="ml-1 text-[10px] text-[var(--color-destructive)]">/{status?.failed_count}</span>
            )}
          </div>
          <div className="text-[10px] text-[var(--color-muted-foreground)]">{t('settings.memory.extracted')}</div>
        </div>
      </div>

      <div className="rounded-[var(--radius-lg)] border border-[var(--color-border)] p-3.5">
        <div className="flex items-center justify-between gap-3">
          <div>
            <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.memory.todayBudget')}</div>
            <div className="mt-0.5 font-mono text-[10px] text-[var(--color-muted-foreground)]">
              {todayTokens.toLocaleString()} / {budget.toLocaleString()} tokens
            </div>
          </div>
          <span className="font-mono text-[11px] text-[var(--color-muted-foreground)]">{budgetPct}%</span>
        </div>
        <div
          role="progressbar"
          aria-label={t('settings.memory.todayBudget')}
          aria-valuemin={0}
          aria-valuemax={budget}
          aria-valuenow={Math.min(todayTokens, budget)}
          aria-valuetext={`${todayTokens.toLocaleString()} / ${budget.toLocaleString()} tokens`}
          className="mt-2 h-1.5 overflow-hidden rounded-full bg-[var(--color-muted)]"
        >
          <div
            className="h-full rounded-full transition-all"
            style={{
              width: `${budgetPct}%`,
              backgroundColor: budgetPct > 90 ? 'var(--color-destructive)' : 'var(--color-accent-neutral)',
            }}
          />
        </div>
        <div className="mt-3 grid grid-cols-2 gap-3 text-[10px] text-[var(--color-muted-foreground)]">
          <div>
            <div>{t('settings.memory.lastPipeline')}</div>
            <div className="mt-0.5 text-[var(--color-foreground)]">{formatTime(lastPipeline)}</div>
          </div>
          <div>
            <div>{t('settings.memory.lastConsolidation')}</div>
            <div className="mt-0.5 text-[var(--color-foreground)]">{formatTime(lastConsolidation)}</div>
          </div>
        </div>
      </div>

      <div>
        <h4 className="text-[12px] font-semibold text-[var(--color-foreground)]">{t('settings.memory.globalSettingsTitle')}</h4>
        <p className="mt-0.5 text-[11px] leading-relaxed text-[var(--color-muted-foreground)]">
          {t('settings.memory.globalSettingsDesc')}
        </p>
      </div>

      <div className="space-y-2">
        <div className={ROW}>
          <div className="min-w-0 flex-1">
            <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.memory.enableTitle')}</div>
            <div className="text-[11px] text-[var(--color-muted-foreground)]">{t('settings.memory.enableDesc')}</div>
          </div>
          <Switch
            on={cfg.enabled}
            onClick={() => patch({ enabled: !cfg.enabled })}
            ariaLabel={t('settings.memory.enableTitle')}
            disabled={controlsDisabled}
          />
        </div>
        <div className={ROW}>
          <div className="min-w-0 flex-1">
            <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.memory.dreamTitle')}</div>
            <div className="text-[11px] text-[var(--color-muted-foreground)]">{t('settings.memory.dreamDesc')}</div>
          </div>
          <Switch
            on={cfg.generate}
            onClick={() => patch({ generate: !cfg.generate })}
            ariaLabel={t('settings.memory.dreamTitle')}
            disabled={controlsDisabled || !cfg.enabled}
          />
        </div>
      </div>

      <Field label={t('settings.memory.model')}>
        <input
          value={cfg.model ?? ''}
          onChange={(e) => patch({ model: e.target.value })}
          disabled={controlsDisabled}
          aria-label={t('settings.memory.model')}
          placeholder={t('settings.memory.modelPlaceholder')}
          className={INPUT_MONO}
        />
        <div className="mt-1 text-[10px] text-[var(--color-muted-foreground)]">{t('settings.memory.modelHint')}</div>
      </Field>

      <button
        type="button"
        className="flex w-full items-center justify-between rounded-[var(--radius-md)] py-1 text-[11px] font-medium text-[var(--color-muted-foreground)] hover:text-[var(--color-foreground)]"
        onClick={() => setAdvanced((v) => !v)}
        aria-expanded={advanced}
      >
        {t('settings.memory.advanced')}
        <ChevronDownIcon className={`h-3.5 w-3.5 transition-transform ${advanced ? 'rotate-180' : ''}`} />
      </button>

      {advanced && (
        <div className="grid grid-cols-2 gap-x-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] p-3.5">
          {([
            ['daily_token_budget', 'dailyBudget', 1, 10000000],
            ['cooldown_hours', 'cooldownHours', 1, 720],
            ['max_age_days', 'maxAgeDays', 1, 3650],
            ['max_unused_days', 'maxUnusedDays', 1, 3650],
            ['phase2_top_n', 'phase2TopN', 1, 1000],
            ['summary_inject_tokens', 'injectTokens', 64, 100000],
          ] as const).map(([field, label, min, max]) => (
            <Field key={field} label={t(`settings.memory.${label}`)}>
              <input
                type="number"
                min={min}
                max={max}
                value={cfg[field] as number}
                disabled={controlsDisabled}
                aria-label={t(`settings.memory.${label}`)}
                onChange={(e) => numberPatch(field, e.target.value)}
                className={INPUT}
              />
            </Field>
          ))}
        </div>
      )}

      {(status?.warning || warning || pollError || (!initialLoadFailed && error) || message) && (
        <div
          role={error && !initialLoadFailed ? 'alert' : 'status'}
          aria-live={error && !initialLoadFailed ? 'assertive' : 'polite'}
          className="flex items-center justify-between gap-3 text-[11px]"
          style={{
            color:
              error && !initialLoadFailed
                ? 'var(--color-destructive)'
                : status?.warning || warning || pollError
                  ? 'var(--color-warning-fg)'
                  : 'var(--color-success)',
          }}
        >
          <span>
            {error && !initialLoadFailed
              ? t('settings.memory.failed', { reason: error })
              : status?.warning || warning || pollError || message}
          </span>
          {pollError && (
            <button type="button" className={`${BTN_SECONDARY} ${BTN_XS}`} onClick={() => void retryPoll()}>
              {t('common.retry')}
            </button>
          )}
        </div>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-[var(--color-border)] pt-4">
        <div className="flex gap-2">
          <button
            type="button"
            className={`${BTN_SECONDARY} ${BTN_SM}`}
            disabled={controlsDisabled || projectUnavailable || dirty || busy || !memoryActive || !generationActive}
            onClick={() => void syncNow()}
          >
            <ArrowPathIcon className={`h-3.5 w-3.5 ${action === 'sync' ? 'animate-spin' : ''}`} />
            {action === 'sync' ? t('settings.memory.syncing') : t('settings.memory.syncNow')}
          </button>
          <button
            type="button"
            className={`${BTN_GHOST} ${BTN_SM} text-[var(--color-destructive)]`}
            disabled={controlsDisabled || projectUnavailable || dirty || busy}
            onClick={() => void clearProject()}
          >
            <TrashIcon className="h-3.5 w-3.5" />
            {action === 'clear' ? t('settings.memory.clearing') : t('settings.memory.clearProject')}
          </button>
        </div>
        <button type="button" className={`${BTN_PRIMARY} ${BTN_SM}`} disabled={controlsDisabled || !dirty} onClick={() => void save()}>
          {saving ? t('common.loading') : t('common.save')}
        </button>
      </div>
    </div>
  )
}

// ════════════════════════════════════════════════════════════════════════════
// Appearance tab — theme picker
// ════════════════════════════════════════════════════════════════════════════

function applyTheme(name: string) {
  const t = THEMES.find((x) => x.id === name)
  const dark = t ? t.appearance === 'dark' : true
  document.documentElement.setAttribute('data-theme', name)
  document.documentElement.classList.toggle('dark', dark)
  try {
    localStorage.setItem('jcode-theme', name)
  } catch {
    /* ignore */
  }
}

function AppearanceTab() {
  const { t } = useTranslation()
  const dispatch = useAppDispatch()
  const storeTheme = useAppSelector((s) => s.ui.theme)
  // Resolve the active theme: prefer store, fall back to localStorage, then default.
  const [active, setActive] = useState(() => {
    let th = storeTheme
    if (!th || th === 'system' || !THEMES.some((x) => x.id === th)) {
      try {
        th = localStorage.getItem('jcode-theme') || 'jcode-dark'
      } catch {
        th = 'jcode-dark'
      }
    }
    return th
  })

  function choose(name: string) {
    setActive(name)
    dispatch(uiActions.setTheme(name))
    applyTheme(name)
    void api.setAccountPreferences({ theme: name }).catch((err) => console.error('Failed to sync theme:', err))
  }

  const darkThemes = THEMES.filter((th) => th.appearance === 'dark')
  const lightThemes = THEMES.filter((th) => th.appearance === 'light')

  const ThemeButton = ({ theme }: { theme: { id: string; label: string; appearance: string } }) => (
    <button
      type="button"
      data-theme={theme.id}
      onClick={() => choose(theme.id)}
      className="cursor-pointer overflow-hidden rounded-[var(--radius-md)] text-left transition-transform active:scale-[0.98]"
      style={{
        border: active === theme.id ? '2px solid var(--color-accent-neutral)' : '1px solid var(--color-border)',
        backgroundColor: 'var(--color-background)',
      }}
    >
      <div className="px-2.5 pb-1.5 pt-2">
        <div className="mb-1.5 flex items-center gap-1.5">
          <span className="h-2.5 w-2.5 rounded-full" style={{ backgroundColor: 'var(--color-primary)' }} />
          <span className="h-2.5 w-2.5 rounded-full" style={{ backgroundColor: 'var(--color-accent-neutral)' }} />
          <span className="h-2.5 w-2.5 rounded-full" style={{ backgroundColor: 'var(--color-success-fg)' }} />
          <span className="h-2.5 w-2.5 rounded-full" style={{ backgroundColor: 'var(--color-error-fg)' }} />
        </div>
        <div
          className="truncate rounded px-1.5 py-1 font-mono text-[10px]"
          style={{ backgroundColor: 'var(--color-surface)', color: 'var(--color-foreground)' }}
        >
          <span style={{ color: 'var(--color-primary)' }}>&gt;</span> jcode
        </div>
      </div>
      <div
        className="flex items-center justify-between px-2.5 py-1.5"
        style={{ backgroundColor: 'var(--color-surface)', borderTop: '1px solid var(--color-border)' }}
      >
        <span className="truncate text-[11px] font-medium text-[var(--color-foreground)]">{theme.label}</span>
        {active === theme.id && (
          <CheckIcon className="h-3.5 w-3.5 shrink-0" style={{ color: 'var(--color-accent-neutral)' }} />
        )}
      </div>
    </button>
  )

  return (
    <div className="space-y-5">
      <h3 className={SECTION_TITLE}>{t('settings.appearance.theme')}</h3>
      <div>
        <div className="mb-2 text-[10px] font-medium text-[var(--color-muted-foreground)]">{t('settings.appearance.dark')}</div>
        <div className="grid grid-cols-2 gap-2">
          {darkThemes.map((th) => (
            <ThemeButton key={th.id} theme={th} />
          ))}
        </div>
      </div>
      <div>
        <div className="mb-2 text-[10px] font-medium text-[var(--color-muted-foreground)]">{t('settings.appearance.light')}</div>
        <div className="grid grid-cols-2 gap-2">
          {lightThemes.map((th) => (
            <ThemeButton key={th.id} theme={th} />
          ))}
        </div>
      </div>
      <div className="text-[10px] leading-relaxed text-[var(--color-muted-foreground)]">
        {t('settings.appearance.terminalHint', { cmd: '/theme' })}
      </div>
    </div>
  )
}

// ════════════════════════════════════════════════════════════════════════════
// Browser tab — config + site permissions
// ════════════════════════════════════════════════════════════════════════════

const BROWSER_BRIDGE_STORE_URL =
  'https://chromewebstore.google.com/detail/jcode-browser-bridge/olkapiiikpfhaccmjphakolinkcggcbd'
const BROWSER_USE_GUIDE_URL = 'https://www.j-code.net/docs/overview/browser-use'

function BrowserTab() {
  const { t } = useTranslation()
  const [status, setStatus] = useState<BrowserStatusResponse | null>(null)
  const [cfg, setCfg] = useState<BrowserConfig>({
    enabled: false,
    backend: 'auto',
    site_permissions: [],
    approval: {},
    dev_mode: false,
  })
  const saveTimer = useRef<number | null>(null)
  const pollRef = useRef<number | null>(null)

  async function load() {
    try {
      const st = await api.browserStatus()
      setStatus(st)
      if (st.status) {
        setCfg({
          enabled: st.status.enabled,
          backend: st.status.backend || 'auto',
          chrome_path: st.status.chrome_path,
          dev_mode: st.status.dev_mode,
          approval: st.approval || {},
          site_permissions: st.site_permissions || [],
        })
      }
    } catch (err) {
      console.error('Failed to load browser status:', err)
    }
  }

  useEffect(() => {
    void load()
    pollRef.current = window.setInterval(() => void load(), 3000)
    return () => {
      if (pollRef.current) window.clearInterval(pollRef.current)
      if (saveTimer.current) window.clearTimeout(saveTimer.current)
    }
  }, [])

  function save(next: BrowserConfig) {
    if (saveTimer.current) window.clearTimeout(saveTimer.current)
    saveTimer.current = window.setTimeout(async () => {
      try {
        await api.browserSaveConfig(next)
        await load()
      } catch (err) {
        console.error('Failed to save browser config:', err)
      }
    }, 250)
  }

  function patch(p: Partial<BrowserConfig>) {
    setCfg((prev) => {
      const next = { ...prev, ...p }
      save(next)
      return next
    })
  }

  function setApproval(cls: string, val: string) {
    const approval = { ...(cfg.approval ?? {}), [cls]: val }
    patch({ approval })
  }

  function addSitePerm() {
    const perms = [...(cfg.site_permissions ?? [])]
    perms.push({ origin: '', navigate: 'allow', interact: 'allow' })
    patch({ site_permissions: perms })
  }

  function removeSitePerm(i: number) {
    const perms = (cfg.site_permissions ?? []).filter((_, j) => j !== i)
    patch({ site_permissions: perms })
  }

  function updateSitePerm(i: number, p: Partial<BrowserSitePermission>) {
    const perms = (cfg.site_permissions ?? []).map((sp, j) => (j === i ? { ...sp, ...p } : sp))
    patch({ site_permissions: perms })
  }

  const st = status?.status

  return (
    <div>
      <div className="mb-4">
        <h3 className={SECTION_TITLE}>{t('settings.browser.title')}</h3>
        <p className="mt-0.5 text-[12px] text-[var(--color-muted-foreground)]">{t('settings.browser.subtitle')}</p>
      </div>

      <div className={ROW}>
        <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
          <GlobeAltIcon className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.browser.enableTitle')}</div>
          <div className="text-[11px] text-[var(--color-muted-foreground)]">{t('settings.browser.enableDesc')}</div>
        </div>
        <Switch on={cfg.enabled} onClick={() => patch({ enabled: !cfg.enabled })} />
      </div>

      {cfg.enabled && (
        <>
          <div className="mb-2 mt-5 text-[11px] font-medium uppercase tracking-wide text-[var(--color-muted-foreground)]">
            {t('settings.browser.control')}
          </div>
          <div className="space-y-2">
            <div className={ROW}>
              <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
                <ComputerDesktopIcon className="h-4 w-4" />
              </div>
              <div className="min-w-0 flex-1">
                <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.browser.managedChrome')}</div>
                <div className="text-[11px] text-[var(--color-muted-foreground)]">
                  {st?.chrome_found ? st.chrome_version || st.chrome_path : t('settings.browser.noChromeDetected')}
                </div>
              </div>
              <select
                value={cfg.backend}
                onChange={(e) => patch({ backend: e.target.value })}
                className={INPUT_SM}
                style={{ width: '8rem' }}
              >
                <option value="auto">{t('settings.browser.backendAuto')}</option>
                <option value="managed">{t('settings.browser.backendManaged')}</option>
                <option value="extension">{t('settings.browser.backendExtension')}</option>
              </select>
            </div>
            <div className={ROW}>
              <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
                <GlobeAltIcon className="h-4 w-4" />
              </div>
              <div className="min-w-0 flex-1">
                <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.browser.extension')}</div>
                <div className="flex items-center gap-1.5 text-[11px] text-[var(--color-muted-foreground)]">
                  <span
                    className="h-1.5 w-1.5 shrink-0 rounded-full"
                    style={{ backgroundColor: st?.extension_online ? 'var(--color-success-fg)' : 'var(--color-border)' }}
                  />
                  {st?.extension_online ? t('settings.browser.connected') : t('settings.browser.notConnected')}
                </div>
              </div>
              {st?.extension_online ? (
                <span className={CHIP + ' !bg-[var(--color-success-bg)] !text-[var(--color-success-fg)]'}>{t('settings.browser.online')}</span>
              ) : (
                <a
                  className={`${BTN_SECONDARY} ${BTN_XS} shrink-0`}
                  href={BROWSER_BRIDGE_STORE_URL}
                  target="_blank"
                  rel="noreferrer"
                >
                  {t('settings.browser.installExtension')}
                  <ArrowTopRightOnSquareIcon className="h-3 w-3" />
                </a>
              )}
            </div>
          </div>

          {!st?.extension_online && (
            <div className="mt-2 flex items-center gap-3 rounded-[var(--radius-md)] border border-[var(--color-border)] bg-[var(--color-muted)] px-3 py-2">
              <p className="min-w-0 flex-1 text-[10.5px] leading-relaxed text-[var(--color-muted-foreground)]">
                {t('settings.browser.connectHint')}
              </p>
              <a
                className="shrink-0 text-[10.5px] font-medium text-[var(--color-accent-neutral)] hover:underline"
                href={BROWSER_USE_GUIDE_URL}
                target="_blank"
                rel="noreferrer"
              >
                {t('settings.browser.setupGuide')}
              </a>
            </div>
          )}

          {!st?.chrome_found && (
            <div className="mt-3">
              <label className="text-[11px] font-medium text-[var(--color-muted-foreground)]">{t('settings.browser.chromePath')}</label>
              <input
                value={cfg.chrome_path ?? ''}
                onChange={(e) => patch({ chrome_path: e.target.value })}
                className={INPUT + ' mt-1'}
                placeholder="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
              />
            </div>
          )}

          <div className="mb-2 mt-5 text-[11px] font-medium uppercase tracking-wide text-[var(--color-muted-foreground)]">
            {t('settings.browser.approval')}
          </div>
          <div className="space-y-2">
            <div className={ROW}>
              <div className="min-w-0 flex-1">
                <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.browser.navigate')}</div>
              </div>
              <select
                value={cfg.approval?.['navigate'] ?? 'ask'}
                onChange={(e) => setApproval('navigate', e.target.value)}
                className={INPUT_SM}
                style={{ width: '10rem' }}
              >
                <option value="ask">{t('settings.browser.askEachSite')}</option>
                <option value="always_allow">{t('settings.browser.alwaysAllow')}</option>
              </select>
            </div>
            <div className={ROW}>
              <div className="min-w-0 flex-1">
                <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.browser.interact')}</div>
              </div>
              <select
                value={cfg.approval?.['interact'] ?? 'ask'}
                onChange={(e) => setApproval('interact', e.target.value)}
                className={INPUT_SM}
                style={{ width: '10rem' }}
              >
                <option value="ask">{t('settings.browser.askEachSite')}</option>
                <option value="always_allow">{t('settings.browser.alwaysAllow')}</option>
              </select>
            </div>
          </div>

          <div className="mb-2 mt-5 flex items-center justify-between">
            <div className="text-[11px] font-medium uppercase tracking-wide text-[var(--color-muted-foreground)]">
              {t('settings.browser.sitePermissions')}
            </div>
            <button type="button" className={`${BTN_SECONDARY} ${BTN_SM}`} onClick={addSitePerm}>
              <PlusIcon className="h-3.5 w-3.5" /> {t('settings.browser.add')}
            </button>
          </div>
          <div className="space-y-2">
            {!(cfg.site_permissions?.length ?? 0) && (
              <div className={ROW}>
                <div className="text-[11px] text-[var(--color-muted-foreground)]">{t('settings.browser.noSitePermissions')}</div>
              </div>
            )}
            {(cfg.site_permissions ?? []).map((sp, i) => (
              <div key={i} className={ROW}>
                <input
                  value={sp.origin}
                  onChange={(e) => updateSitePerm(i, { origin: e.target.value })}
                  className={INPUT_SM}
                  style={{ flex: 1 }}
                  placeholder="https://github.com"
                />
                <select
                  value={sp.navigate ?? 'ask'}
                  onChange={(e) => updateSitePerm(i, { navigate: e.target.value })}
                  className={INPUT_SM}
                  style={{ width: '7rem' }}
                >
                  <option value="ask">{t('settings.browser.navAsk')}</option>
                  <option value="allow">{t('settings.browser.navAllow')}</option>
                </select>
                <select
                  value={sp.interact ?? 'ask'}
                  onChange={(e) => updateSitePerm(i, { interact: e.target.value })}
                  className={INPUT_SM}
                  style={{ width: '7rem' }}
                >
                  <option value="ask">{t('settings.browser.actAsk')}</option>
                  <option value="allow">{t('settings.browser.actAllow')}</option>
                </select>
                <button type="button" className={`${BTN_GHOST} ${BTN_SM}`} onClick={() => removeSitePerm(i)}>
                  <TrashIcon className="h-3.5 w-3.5" />
                </button>
              </div>
            ))}
          </div>

          <div className="mb-2 mt-5 text-[11px] font-medium uppercase tracking-wide text-[var(--color-muted-foreground)]">
            {t('settings.browser.developerMode')}
          </div>
          <div className={ROW}>
            <div className="min-w-0 flex-1">
              <div className="mb-0.5 text-[11px] font-semibold text-[var(--color-warning-fg)]">⚠ {t('settings.browser.elevatedRisk')}</div>
              <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.browser.devModeTitle')}</div>
              <div className="text-[11px] text-[var(--color-muted-foreground)]">
                {t('settings.browser.devModeDesc')}
              </div>
            </div>
            <Switch on={!!cfg.dev_mode} onClick={() => patch({ dev_mode: !cfg.dev_mode })} />
          </div>
        </>
      )}
    </div>
  )
}

// ════════════════════════════════════════════════════════════════════════════
// Computer tab — config + app permissions + grants
// ════════════════════════════════════════════════════════════════════════════
//
// Mirrors BrowserTab's poll + debounced-save shape, with two deliberate
// differences for a native, macOS-only capability:
//
//  1. Readiness renders even when the feature is off. Helper installation,
//     Accessibility, and Screen Recording are separate facts; an unknown TCC
//     state is never presented as ready.
//  2. Polling refreshes *status* unconditionally but only re-syncs *config*
//     when there is no local edit in flight. The user leaves this page to grant
//     a TCC permission and comes back expecting the status to have noticed —
//     but a 3s poll must not overwrite a half-typed bundle id.

type Tier = 'read' | 'click' | 'full'

const TIER_ORDER: Tier[] = ['read', 'click', 'full']
const TIER_RANK: Record<Tier, number> = { read: 0, click: 1, full: 2 }

function isTier(v: string | undefined | null): v is Tier {
  return v === 'read' || v === 'click' || v === 'full'
}

// The tier badge is this page's one new visual primitive. Colors are semantic
// and taken from the existing token contract — no new palette:
//   read  → neutral wash + muted text (slate: observation only, unremarkable)
//   click → the warning tokens        (amber: it can now touch things)
//   full  → accent wash + primary     (accent: it can type; this is the ceiling)
const TIER_BADGE: Record<Tier, string> = {
  read: CHIP + ' !bg-[var(--neutral-wash)] !text-[var(--color-muted-foreground)]',
  click: CHIP + ' !bg-[var(--color-warning-bg)] !text-[var(--color-warning-fg)]',
  full: CHIP + ' !bg-[var(--accent-wash)] !text-[var(--color-primary)]',
}

function TierBadge({ tier, locked, title }: { tier: Tier; locked?: boolean; title?: string }) {
  const { t } = useTranslation()
  return (
    <span className={TIER_BADGE[tier] + ' shrink-0'} title={title}>
      {locked && <LockClosedIcon className="h-2.5 w-2.5 shrink-0" />}
      {t(`settings.computer.tier.${tier}`)}
    </span>
  )
}

const ACCESSIBILITY_DEEP_LINK = 'x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility'
const SCREEN_RECORDING_DEEP_LINK = 'x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture'

const EMPTY_COMPUTER_CONFIG: ComputerConfig = {
  enabled: false,
  approval: {},
  app_permissions: [],
  clipboard_read: false,
  clipboard_write: false,
  system_key_combos: false,
}

type ComputerSaveState = 'idle' | 'saving' | 'saved' | 'error'

function normalizeComputerConfig(input: ComputerConfig): ComputerConfig {
  return {
    ...input,
    enabled: !!input.enabled,
    approval: input.approval ?? {},
    app_permissions: input.app_permissions ?? [],
    clipboard_read: !!input.clipboard_read,
    clipboard_write: !!input.clipboard_write,
    system_key_combos: !!input.system_key_combos,
  }
}

function ComputerPermissionRow({
  label,
  description,
  state,
  href,
}: {
  label: string
  description: string
  state: ComputerPermissionState
  href: string
}) {
  const { t } = useTranslation()
  const [openError, setOpenError] = useState(false)
  const Icon = state === 'granted' ? CheckIcon : state === 'denied' ? XMarkIcon : MinusIcon
  const iconStyle =
    state === 'granted'
      ? { background: 'var(--color-success-bg)', color: 'var(--color-success-fg)' }
      : state === 'denied'
        ? { background: 'var(--color-warning-bg)', color: 'var(--color-warning-fg)' }
        : { background: 'var(--neutral-wash)', color: 'var(--color-muted-foreground)' }

  return (
    <div className="flex items-center gap-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3.5 py-3">
      <span className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)]" style={iconStyle}>
        <Icon className="h-3.5 w-3.5" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-[12px] font-medium text-[var(--color-foreground)]">{label}</div>
        <div className="mt-0.5 text-[10.5px] leading-relaxed text-[var(--color-muted-foreground)]">{description}</div>
        {openError && (
          <div className="mt-1 text-[10.5px] leading-relaxed text-[var(--color-error-fg)]">
            {t('settings.computer.openSystemSettingsFailed')}
          </div>
        )}
      </div>
      <span className={CHIP + ' shrink-0'}>{t(`settings.computer.permissionState.${state}`)}</span>
      {state !== 'granted' && (
        <button
          type="button"
          className={`${BTN_GHOST} ${BTN_XS} shrink-0`}
          title={t('settings.computer.openSystemSettingsHint')}
          onClick={() => {
            setOpenError(false)
            void openUrl(href).catch((err) => {
              console.error('Failed to open macOS System Settings:', err)
              setOpenError(true)
            })
          }}
        >
          {t('settings.computer.openSystemSettings')}
        </button>
      )}
    </div>
  )
}

function ComputerTab() {
  const { t } = useTranslation()
  const [status, setStatus] = useState<ComputerStatusResponse | null>(null)
  const [cfg, setCfg] = useState<ComputerConfig>({ ...EMPTY_COMPUTER_CONFIG })
  const [saveState, setSaveState] = useState<ComputerSaveState>('idle')
  const [saveError, setSaveError] = useState('')
  const [saveWarning, setSaveWarning] = useState('')
  const [loadError, setLoadError] = useState('')
  const [checking, setChecking] = useState(false)
  const [requesting, setRequesting] = useState(false)
  const [requestFailed, setRequestFailed] = useState(false)
  // A pending loosen, held until the user clicks through the warning.
  const [loosen, setLoosen] = useState<{ i: number; tier: Tier } | null>(null)
  const saveTimer = useRef<number | null>(null)
  const saveResetTimer = useRef<number | null>(null)
  const pollRef = useRef<number | null>(null)
  const cfgRef = useRef<ComputerConfig>({ ...EMPTY_COMPUTER_CONFIG })
  const dirtyRef = useRef(false)
  const configEpochRef = useRef(0)
  const loadInFlightRef = useRef<Promise<void> | null>(null)
  const saveInFlightRef = useRef(false)

  const startLoad = useCallback((): Promise<void> => {
    const requestEpoch = configEpochRef.current
    const request = (async () => {
      try {
        const response = await api.computerStatus()
        // A GET can spend seconds probing the helper. If a POST committed while
        // it was in flight, both its status and config are stale; the forced
        // post-save load below will replace it.
        if (requestEpoch !== configEpochRef.current) return
        setLoadError('')
        setStatus(response)
        if (!dirtyRef.current) {
          const canonical = normalizeComputerConfig(response.config)
          cfgRef.current = canonical
          setCfg(canonical)
        }
      } catch (err) {
        if (requestEpoch !== configEpochRef.current) return
        console.error('Failed to load computer status:', err)
        setLoadError(err instanceof Error ? err.message : String(err))
      }
    })()
    loadInFlightRef.current = request
    void request.finally(() => {
      if (loadInFlightRef.current === request) loadInFlightRef.current = null
    })
    return request
  }, [])

  const load = useCallback(
    async (forceAfterInflight = false) => {
      const active = loadInFlightRef.current
      if (active) {
        await active
        if (!forceAfterInflight) return
      }
      // A second caller may have started a request while this one awaited. A
      // manual/post-save refresh waits it out, then always starts a fresh GET.
      while (loadInFlightRef.current) await loadInFlightRef.current
      await startLoad()
    },
    [startLoad],
  )

  useEffect(() => {
    void load()
    pollRef.current = window.setInterval(() => void load(), 3000)
    return () => {
      if (pollRef.current) window.clearInterval(pollRef.current)
      if (saveTimer.current) window.clearTimeout(saveTimer.current)
      if (saveResetTimer.current) window.clearTimeout(saveResetTimer.current)
    }
  }, [load])

  function save(next: ComputerConfig) {
    if (!status || saveInFlightRef.current) return
    dirtyRef.current = true
    if (saveTimer.current) window.clearTimeout(saveTimer.current)
    if (saveResetTimer.current) window.clearTimeout(saveResetTimer.current)
    setSaveError('')
    setSaveWarning('')
    setSaveState('idle')
    saveTimer.current = window.setTimeout(async () => {
      saveTimer.current = null
      if (saveInFlightRef.current) return
      saveInFlightRef.current = true
      setSaveState('saving')
      try {
        const response = await api.computerSaveConfig(next)
        const canonical = normalizeComputerConfig(response.config)
        configEpochRef.current++
        dirtyRef.current = false
        cfgRef.current = canonical
        setCfg(canonical)
        // This must be a new request even when the 3-second poll is still in
        // flight; otherwise the button could claim success while showing stale
        // helper/permission state.
        await load(true)
        setSaveWarning(response.warning_code ?? '')
        setSaveState('saved')
        if (!response.warning_code) {
          saveResetTimer.current = window.setTimeout(() => setSaveState('idle'), 1800)
        }
      } catch (err) {
        console.error('Failed to save computer config:', err)
        setSaveError(err instanceof Error ? err.message : String(err))
        setSaveState('error')
      } finally {
        saveInFlightRef.current = false
      }
    }, 250)
  }

  async function checkAgain() {
    if (checking) return
    setChecking(true)
    try {
      await load(true)
    } finally {
      setChecking(false)
    }
  }

  /** One click surfaces the macOS consent prompts for both grants (Codex-style:
   *  a single request, not one per permission). The system dialogs are answered
   *  outside this flow, so states usually stay "denied" until the user acts in
   *  them — the 3s poll observes the flips. */
  async function requestPermissions() {
    if (requesting) return
    setRequesting(true)
    setRequestFailed(false)
    try {
      await api.computerRequestPermissions({ accessibility: true, screen_recording: true })
      await load(true)
    } catch (err) {
      console.error('Failed to request macOS permissions:', err)
      setRequestFailed(true)
    } finally {
      setRequesting(false)
    }
  }

  function patch(p: Partial<ComputerConfig>) {
    if (!status || saveInFlightRef.current) return
    const next = { ...cfgRef.current, ...p }
    cfgRef.current = next
    setCfg(next)
    save(next)
  }

  function setApproval(cls: string, val: string) {
    patch({ approval: { ...(cfgRef.current.approval ?? {}), [cls]: val } })
  }

  function addAppPerm() {
    patch({ app_permissions: [...(cfgRef.current.app_permissions ?? []), { bundle_id: '', launch: 'ask', interact: 'ask' }] })
  }

  function removeAppPerm(i: number) {
    setLoosen(null)
    patch({ app_permissions: (cfgRef.current.app_permissions ?? []).filter((_, j) => j !== i) })
  }

  function updateAppPerm(i: number, p: Partial<ComputerAppPermission>) {
    patch({ app_permissions: (cfgRef.current.app_permissions ?? []).map((ap, j) => (j === i ? { ...ap, ...p } : ap)) })
  }

  const st = status?.status

  /** The built-in tier for a bundle id, straight from the server's table.
   *  null = not known yet (empty id, or a freshly typed one that has not round-
   *  tripped). We render that as pending rather than guessing: guessing "full"
   *  would briefly offer options that a terminal can never actually have. */
  function builtinTier(bundleID: string): Tier | null {
    const id = bundleID.trim()
    if (!id) return null
    const raw = st?.tiers?.[id]
    return isTier(raw) ? raw : null
  }

  /** What the app *actually* runs at. An override may only tighten, so anything
   *  looser than the built-in tier is clamped away here exactly as
   *  computer.Manager.TierOverrides drops it — the badge must show the truth,
   *  not a stored-but-ignored wish. */
  function effectiveTier(p: ComputerAppPermission, builtin: Tier): Tier {
    const want = isTier(p.tier) ? p.tier : builtin
    return TIER_RANK[want] < TIER_RANK[builtin] ? want : builtin
  }

  /** Why a family is capped, keyed off the built-in tier alone (browsers are the
   *  only read family, terminals/IDEs the only click family), so this never
   *  re-implements the bundle-id tables in internal/computer/tiers.go. */
  function lockReason(builtin: Tier): string | undefined {
    if (builtin === 'read') return t('settings.computer.whyBrowser')
    if (builtin === 'click') return t('settings.computer.whyTerminal')
    return undefined
  }

  function requestTier(i: number, tier: Tier) {
    const p = (cfg.app_permissions ?? [])[i]
    const builtin = p && builtinTier(p.bundle_id)
    if (!p || !builtin) return
    // Tightening is free; loosening is the direction that needs a deliberate act.
    if (TIER_RANK[tier] > TIER_RANK[effectiveTier(p, builtin)]) {
      setLoosen({ i, tier })
      return
    }
    applyTier(i, tier, builtin)
  }

  function applyTier(i: number, tier: Tier, builtin: Tier) {
    setLoosen(null)
    // Store "" when the choice is just the built-in default: an override that
    // restates the table is noise, and the backend drops it anyway.
    updateAppPerm(i, { tier: tier === builtin ? '' : tier })
  }

  const saveBusy = saveState === 'saving'

  if (status && !status.supported) {
    return (
      <div>
        <div className="mb-4">
          <h3 className={SECTION_TITLE}>{t('settings.computer.title')}</h3>
          <p className="mt-0.5 text-[12px] text-[var(--color-muted-foreground)]">{t('settings.computer.subtitle')}</p>
        </div>
        <div className="rounded-[var(--radius-xl)] border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
          <div className="flex items-start gap-3">
            <div className="grid h-9 w-9 shrink-0 place-items-center rounded-[var(--radius-lg)] bg-[var(--neutral-wash)] text-[var(--color-muted-foreground)]">
              <ComputerDesktopIcon className="h-[18px] w-[18px]" />
            </div>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <div className="text-[13px] font-semibold text-[var(--color-foreground)]">
                  {t('settings.computer.macosOnlyTitle')}
                </div>
                <span className={CHIP}>macOS 14+</span>
              </div>
              <p className="mt-1 text-[11px] leading-relaxed text-[var(--color-muted-foreground)]">
                {t('settings.computer.macosOnlyDesc', { platform: status.platform || t('settings.computer.unknownPlatform') })}
              </p>
            </div>
          </div>
        </div>
      </div>
    )
  }

  if (!status && loadError) {
    return (
      <div>
        <div className="mb-4">
          <h3 className={SECTION_TITLE}>{t('settings.computer.title')}</h3>
          <p className="mt-0.5 text-[12px] text-[var(--color-muted-foreground)]">{t('settings.computer.subtitle')}</p>
        </div>
        <div className="rounded-[var(--radius-lg)] border border-[var(--color-error-fg)] bg-[var(--color-error-bg)] p-3.5">
          <div className="flex items-start gap-3">
            <XMarkIcon className="mt-0.5 h-4 w-4 shrink-0 text-[var(--color-error-fg)]" />
            <div className="min-w-0 flex-1">
              <div className="text-[12px] font-medium text-[var(--color-error-fg)]">
                {t('settings.computer.statusLoadFailed')}
              </div>
              <div className="mt-1 break-words font-mono text-[10.5px] text-[var(--color-muted-foreground)]">{loadError}</div>
            </div>
            <button type="button" className={`${BTN_SECONDARY} ${BTN_SM} shrink-0`} onClick={() => void checkAgain()} disabled={checking}>
              <ArrowPathIcon className={`h-3.5 w-3.5 ${checking ? 'animate-spin' : ''}`} />
              {checking ? t('settings.computer.checking') : t('settings.computer.checkAgain')}
            </button>
          </div>
        </div>
      </div>
    )
  }

  const perms = cfg.app_permissions ?? []
  const accessibility = st?.accessibility ?? 'unknown'
  const screenRecording = st?.screen_recording ?? 'unknown'
  // Unknown is deliberately not optimistic: pixels are a first-class part of
  // computer use, so both TCC grants must be positively known before we say ready.
  const permissionsReady = accessibility === 'granted' && screenRecording === 'granted'
  const helperInstalled = !!st?.helper?.installed
  const helperConnected = !!st?.helper?.connected
  const helperReady = helperConnected
  const ready = !!st && cfg.enabled && st.available && helperReady && permissionsReady && !st.blocker
  const readinessDetail = !st
    ? t('settings.computer.statusLoading')
    : !cfg.enabled
      ? t('settings.computer.offHint')
      : !helperInstalled
        ? t('settings.computer.helperMissingHint')
        : !helperConnected
          ? t('settings.computer.helperDisconnectedHint')
          : accessibility === 'unknown' || screenRecording === 'unknown'
            ? t('settings.computer.permissionsUnknownHint')
            : !permissionsReady
              ? t('settings.computer.permissionsHint')
              : t('settings.computer.readyHint')

  return (
    <div>
      <div className="mb-4 flex min-h-9 items-start justify-between gap-4">
        <div>
          <h3 className={SECTION_TITLE}>{t('settings.computer.title')}</h3>
          <p className="mt-0.5 text-[12px] text-[var(--color-muted-foreground)]">{t('settings.computer.subtitle')}</p>
        </div>
        <div aria-live="polite" className="flex min-h-6 shrink-0 items-center gap-1.5 text-[10.5px]">
          {saveState === 'saving' && (
            <span className="inline-flex items-center gap-1.5 text-[var(--color-muted-foreground)]">
              <ArrowPathIcon className="h-3.5 w-3.5 animate-spin" /> {t('settings.computer.saving')}
            </span>
          )}
          {saveState === 'saved' && (
            <span
              className={`inline-flex max-w-72 items-center gap-1.5 ${saveWarning ? 'text-[var(--color-warning-fg)]' : 'text-[var(--color-success-fg)]'}`}
            >
              {saveWarning ? <ExclamationTriangleIcon className="h-3.5 w-3.5 shrink-0" /> : <CheckIcon className="h-3.5 w-3.5 shrink-0" />}
              <span className="truncate">{saveWarning ? t('settings.computer.savedWithWarning') : t('settings.computer.saved')}</span>
            </span>
          )}
          {saveState === 'error' && (
            <>
              <span className="inline-flex max-w-52 items-center gap-1.5 truncate text-[var(--color-error-fg)]" title={saveError}>
                <XMarkIcon className="h-3.5 w-3.5 shrink-0" /> {t('settings.computer.saveFailed')}
              </span>
              <button type="button" className={`${BTN_GHOST} ${BTN_XS}`} onClick={() => save(cfgRef.current)} disabled={saveBusy}>
                {t('settings.computer.retrySave')}
              </button>
            </>
          )}
        </div>
      </div>

      {saveWarning && (
        <div className="mb-3 flex items-start gap-2.5 rounded-[var(--radius-lg)] border border-[var(--color-warning-fg)] bg-[var(--color-warning-bg)] px-3.5 py-3 text-[11px] text-[var(--color-warning-fg)]">
          <ExclamationTriangleIcon className="mt-0.5 h-4 w-4 shrink-0" />
          <span className="min-w-0 flex-1 leading-relaxed">{t('settings.computer.agentRefreshWarning')}</span>
          <button
            type="button"
            className={`${BTN_GHOST} ${BTN_XS} shrink-0`}
            title={t('common.close')}
            onClick={() => {
              setSaveWarning('')
              setSaveState('idle')
            }}
          >
            <XMarkIcon className="h-3.5 w-3.5" />
          </button>
        </div>
      )}

      <div className={ROW}>
        <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
          <ComputerDesktopIcon className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.computer.enableTitle')}</div>
          <div className="text-[11px] text-[var(--color-muted-foreground)]">{t('settings.computer.enableDesc')}</div>
        </div>
        <Switch on={cfg.enabled} onClick={() => patch({ enabled: !cfg.enabled })} disabled={saveBusy || !status} />
      </div>

      <div className="mb-2 mt-5 flex items-center justify-between gap-3">
        <div className="text-[11px] font-medium uppercase tracking-wide text-[var(--color-muted-foreground)]">
          {t('settings.computer.readiness')}
        </div>
        <span
          className="inline-flex h-[20px] items-center gap-1 rounded-full px-2 text-[10px] font-semibold"
          style={
            ready
              ? { background: 'var(--color-success-bg)', color: 'var(--color-success-fg)' }
              : cfg.enabled && st
                ? { background: 'var(--color-warning-bg)', color: 'var(--color-warning-fg)' }
                : { background: 'var(--neutral-wash)', color: 'var(--color-muted-foreground)' }
          }
        >
          {ready ? <CheckIcon className="h-3 w-3" /> : cfg.enabled && st ? <XMarkIcon className="h-3 w-3" /> : <MinusIcon className="h-3 w-3" />}
          {!st
            ? t('settings.computer.statusLoading')
            : !cfg.enabled
              ? t('settings.computer.readinessOff')
              : ready
                ? t('settings.computer.readinessReady')
                : t('settings.computer.readinessNeedsAttention')}
        </span>
      </div>
      <div className="space-y-2">
        <div className="flex items-center gap-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3.5 py-3">
          <span
            className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)]"
            style={
              !st
                ? { background: 'var(--neutral-wash)', color: 'var(--color-muted-foreground)' }
                : helperConnected
                  ? { background: 'var(--color-success-bg)', color: 'var(--color-success-fg)' }
                  : helperInstalled
                    ? { background: 'var(--color-warning-bg)', color: 'var(--color-warning-fg)' }
                    : { background: 'var(--color-error-bg)', color: 'var(--color-error-fg)' }
            }
          >
            {!st ? (
              <MinusIcon className="h-3.5 w-3.5" />
            ) : helperConnected ? (
              <CheckIcon className="h-3.5 w-3.5" />
            ) : helperInstalled ? (
              <MinusIcon className="h-3.5 w-3.5" />
            ) : (
              <XMarkIcon className="h-3.5 w-3.5" />
            )}
          </span>
          <div className="min-w-0 flex-1">
            <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.computer.nativeHelper')}</div>
            <div className="mt-0.5 text-[10.5px] text-[var(--color-muted-foreground)]">
              {!st
                ? t('settings.computer.statusLoading')
                : st.helper.connected
                  ? t('settings.computer.helperConnected')
                  : st.helper.installed
                    ? t('settings.computer.helperInstalled')
                    : t('settings.computer.helperMissing')}
            </div>
          </div>
          {st?.helper.version && <span className={CHIP + ' shrink-0 font-mono'}>{st.helper.version}</span>}
        </div>

        <ComputerPermissionRow
          label={t('settings.computer.accessibility')}
          description={t('settings.computer.accessibilityDesc')}
          state={accessibility}
          href={ACCESSIBILITY_DEEP_LINK}
        />
        <ComputerPermissionRow
          label={t('settings.computer.screenRecording')}
          description={t('settings.computer.screenRecordingDesc')}
          state={screenRecording}
          href={SCREEN_RECORDING_DEEP_LINK}
        />

        <div className="flex items-start justify-between gap-3 px-1 pt-1">
          <div
            className={`min-w-0 text-[10.5px] leading-relaxed ${loadError || requestFailed ? 'text-[var(--color-error-fg)]' : 'text-[var(--color-muted-foreground)]'}`}
          >
            {loadError
              ? `${t('settings.computer.statusLoadFailed')}: ${loadError}`
              : requestFailed
                ? t('settings.computer.requestPermissionFailed')
                : readinessDetail}
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            {helperConnected && !permissionsReady && (
              <button
                type="button"
                className={`${BTN_PRIMARY} ${BTN_SM} shrink-0`}
                onClick={() => void requestPermissions()}
                disabled={requesting || checking || saveBusy}
              >
                {requesting ? t('settings.computer.requestingPermission') : t('settings.computer.requestPermissions')}
              </button>
            )}
            <button
              type="button"
              className={`${BTN_SECONDARY} ${BTN_SM} shrink-0`}
              onClick={() => void checkAgain()}
              disabled={checking || saveBusy || requesting}
            >
              <ArrowPathIcon className={`h-3.5 w-3.5 ${checking ? 'animate-spin' : ''}`} />
              {checking ? t('settings.computer.checking') : t('settings.computer.checkAgain')}
            </button>
          </div>
        </div>
      </div>

      {cfg.enabled && (
        <>
          {/* ── Approval defaults ─────────────────────────────────────────────
              The baseline the per-app rows below override. Clipboard is absent
              on purpose: reading it always prompts and is not pre-approvable
              (design §4.4) — it holds passwords too often. */}
          <div className="mb-2 mt-5 text-[11px] font-medium uppercase tracking-wide text-[var(--color-muted-foreground)]">
            {t('settings.computer.approval')}
          </div>
          <div className="space-y-2">
            {(['launch', 'interact'] as const).map((cls) => (
              <div key={cls} className={ROW}>
                <div className="min-w-0 flex-1">
                  <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t(`settings.computer.${cls}`)}</div>
                </div>
                <select
                  value={cfg.approval?.[cls] ?? 'ask'}
                  onChange={(e) => setApproval(cls, e.target.value)}
                  disabled={saveBusy}
                  className={INPUT_SM}
                  style={{ width: '10rem' }}
                >
                  <option value="ask">{t('settings.computer.askEachApp')}</option>
                  <option value="always_allow">{t('settings.computer.alwaysAllow')}</option>
                </select>
              </div>
            ))}
          </div>
          <div className="mt-2 text-[10.5px] leading-relaxed text-[var(--color-muted-foreground)]">
            {t('settings.computer.clipboardAlwaysAsks')}
          </div>

          {/* ── App permissions ──────────────────────────────────────────── */}
          <div className="mb-2 mt-5 flex items-center justify-between">
            <div className="text-[11px] font-medium uppercase tracking-wide text-[var(--color-muted-foreground)]">
              {t('settings.computer.appPermissions')}
            </div>
            <button type="button" className={`${BTN_SECONDARY} ${BTN_SM}`} onClick={addAppPerm} disabled={saveBusy}>
              <PlusIcon className="h-3.5 w-3.5" /> {t('settings.computer.add')}
            </button>
          </div>
          <div className="space-y-2">
            {!perms.length && (
              <div className={ROW}>
                <div className="text-[11px] text-[var(--color-muted-foreground)]">{t('settings.computer.noAppPermissions')}</div>
              </div>
            )}
            {perms.map((p, i) => {
              const builtin = builtinTier(p.bundle_id)
              const eff = builtin ? effectiveTier(p, builtin) : null
              const why = builtin ? lockReason(builtin) : undefined
              // Never offer a tier above the built-in one: the backend would
              // drop it and the user would be left believing it took effect.
              const opts = builtin ? TIER_ORDER.filter((x) => TIER_RANK[x] <= TIER_RANK[builtin]) : []
              return (
                <div key={i} className="space-y-1">
                  <div className={ROW}>
                    <input
                      value={p.bundle_id}
                      onChange={(e) => updateAppPerm(i, { bundle_id: e.target.value })}
                      disabled={saveBusy}
                      className={INPUT_SM + ' font-mono'}
                      style={{ flex: 1, minWidth: '8rem' }}
                      placeholder={t('settings.computer.bundlePlaceholder')}
                    />
                    {builtin && eff ? (
                      <TierBadge tier={eff} locked={builtin !== 'full'} title={why ?? t(`settings.computer.tierDesc.${eff}`)} />
                    ) : (
                      <span className={CHIP + ' shrink-0'} title={t('settings.computer.tierPending')}>
                        —
                      </span>
                    )}
                    <select
                      value={eff ?? ''}
                      disabled={saveBusy || !builtin || opts.length <= 1}
                      onChange={(e) => requestTier(i, e.target.value as Tier)}
                      className={INPUT_SM}
                      style={{ width: '5.75rem' }}
                      title={!builtin ? t('settings.computer.tierPending') : opts.length <= 1 ? why : t('settings.computer.tierLabel')}
                    >
                      {!builtin && <option value="">—</option>}
                      {opts.map((x) => (
                        <option key={x} value={x}>
                          {t(`settings.computer.tier.${x}`)}
                        </option>
                      ))}
                    </select>
                    <select
                      value={p.launch ?? 'ask'}
                      onChange={(e) => updateAppPerm(i, { launch: e.target.value })}
                      disabled={saveBusy}
                      className={INPUT_SM}
                      style={{ width: '6.5rem' }}
                    >
                      <option value="ask">{t('settings.computer.launchAsk')}</option>
                      <option value="allow">{t('settings.computer.launchAllow')}</option>
                    </select>
                    <select
                      value={p.interact ?? 'ask'}
                      onChange={(e) => updateAppPerm(i, { interact: e.target.value })}
                      disabled={saveBusy}
                      className={INPUT_SM}
                      style={{ width: '6.5rem' }}
                    >
                      <option value="ask">{t('settings.computer.interactAsk')}</option>
                      <option value="allow">{t('settings.computer.interactAllow')}</option>
                    </select>
                    <button type="button" className={`${BTN_GHOST} ${BTN_SM}`} onClick={() => removeAppPerm(i)} disabled={saveBusy}>
                      <TrashIcon className="h-3.5 w-3.5" />
                    </button>
                  </div>

                  {/* The "why" for a capped family, spelled out rather than left
                      to a hover: an unexplained restriction just reads as jcode
                      being annoying, and the user overrides on reflex. */}
                  {why && (
                    <div className="flex items-start gap-1.5 px-3.5 text-[10.5px] leading-relaxed text-[var(--color-muted-foreground)]">
                      <LockClosedIcon className="mt-[2px] h-3 w-3 shrink-0" />
                      <span>{why}</span>
                    </div>
                  )}

                  {loosen?.i === i && builtin && (
                    <div className="rounded-[var(--radius-md)] border border-[var(--color-warning-fg)] bg-[var(--color-warning-bg)] p-2.5">
                      <div className="text-[12px] font-semibold text-[var(--color-warning-fg)]">
                        ⚠ {t('settings.computer.loosenTitle')}
                      </div>
                      <div className="mt-0.5 text-[10.5px] leading-relaxed text-[var(--color-muted-foreground)]">
                        {t('settings.computer.loosenBody', {
                          app: p.bundle_id || t('settings.computer.thisApp'),
                          what: t(`settings.computer.tierDesc.${loosen.tier}`),
                        })}
                      </div>
                      <div className="mt-2 flex justify-end gap-1.5">
                        <button type="button" className={`${BTN_GHOST} ${BTN_XS}`} onClick={() => setLoosen(null)} disabled={saveBusy}>
                          {t('common.cancel')}
                        </button>
                        <button
                          type="button"
                          className={`${BTN_SECONDARY} ${BTN_XS}`}
                          onClick={() => applyTier(i, loosen.tier, builtin)}
                          disabled={saveBusy}
                        >
                          {t('settings.computer.loosenConfirm')}
                        </button>
                      </div>
                    </div>
                  )}
                </div>
              )
            })}
          </div>
          <div className="mt-2 text-[10.5px] leading-relaxed text-[var(--color-muted-foreground)]">
            {t('settings.computer.tierCeilingNote')}
          </div>

          {/* ── Grants ───────────────────────────────────────────────────────
              Orthogonal to the app allowlist, and each caption says why. */}
          <div className="mb-2 mt-5 text-[11px] font-medium uppercase tracking-wide text-[var(--color-muted-foreground)]">
            {t('settings.computer.grants')}
          </div>
          <div className="mb-2 text-[10.5px] leading-relaxed text-[var(--color-muted-foreground)]">
            {t('settings.computer.grantsDesc')}
          </div>
          <div className="space-y-2">
            <div className={ROW}>
              <div className="min-w-0 flex-1">
                <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.computer.clipboardRead')}</div>
                <div className="text-[11px] leading-relaxed text-[var(--color-muted-foreground)]">
                  {t('settings.computer.clipboardReadDesc')}
                </div>
              </div>
              <Switch on={!!cfg.clipboard_read} onClick={() => patch({ clipboard_read: !cfg.clipboard_read })} disabled={saveBusy} />
            </div>
            <div className={ROW}>
              <div className="min-w-0 flex-1">
                <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.computer.clipboardWrite')}</div>
                <div className="text-[11px] leading-relaxed text-[var(--color-muted-foreground)]">
                  {t('settings.computer.clipboardWriteDesc')}
                </div>
              </div>
              <Switch on={!!cfg.clipboard_write} onClick={() => patch({ clipboard_write: !cfg.clipboard_write })} disabled={saveBusy} />
            </div>
            <div className={ROW}>
              <div className="min-w-0 flex-1">
                <div className="text-[12px] font-medium text-[var(--color-foreground)]">{t('settings.computer.systemKeyCombos')}</div>
                <div className="text-[11px] leading-relaxed text-[var(--color-muted-foreground)]">
                  {t('settings.computer.systemKeyCombosDesc')}
                </div>
              </div>
              <Switch on={!!cfg.system_key_combos} onClick={() => patch({ system_key_combos: !cfg.system_key_combos })} disabled={saveBusy} />
            </div>
          </div>
        </>
      )}
    </div>
  )
}

// ════════════════════════════════════════════════════════════════════════════
// SSH tab — SSH aliases + remote-connect wizard entrypoint
// ════════════════════════════════════════════════════════════════════════════

function SSHTab() {
  const { t } = useTranslation()
  const [data, setData] = useState<SSHListResponse | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    api
      .sshList()
      .then(setData)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  const aliases = data?.aliases ?? []
  const current = data?.current ?? 'local'

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <h3 className={SECTION_TITLE}>{t('settings.ssh.title')}</h3>
        <button type="button" className={`${BTN_SECONDARY} ${BTN_SM}`} onClick={() => openRemoteConnect()}>
          <PlusIcon className="h-3.5 w-3.5" /> {t('settings.ssh.connect')}
        </button>
      </div>

      <div className="mb-3">
        <div className="mb-1 text-[11px] font-medium text-[var(--color-muted-foreground)]">{t('settings.ssh.currentEnv')}</div>
        <span className={CHIP_ACCENT}>
          <span className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: 'var(--color-accent-neutral)' }} />
          {current}
        </span>
      </div>

      {loading ? (
        <div className="animate-pulse py-6 text-center text-xs text-[var(--color-muted-foreground)]">{t('common.loading')}</div>
      ) : aliases.length === 0 ? (
        <EmptyState
          Icon={CommandLineIcon}
          title={t('settings.ssh.noneAliases')}
          hint={t('settings.ssh.noneAliasesHint')}
        />
      ) : (
        <div className="space-y-2">
          {aliases.map((alias) => (
            <div key={alias.name} className={ROW} style={{ cursor: 'default' }}>
              <div className="grid h-7 w-7 shrink-0 place-items-center rounded-[var(--radius-md)] text-[var(--color-muted-foreground)]">
                <ServerIcon className="h-3.5 w-3.5" />
              </div>
              <div className="min-w-0 flex-1">
                <div className="text-[12px] font-medium text-[var(--color-foreground)]">{alias.name}</div>
                <div className="truncate font-mono text-[11px] text-[var(--color-muted-foreground)]">
                  {alias.addr}
                  {alias.path ? ` · ${alias.path}` : ''}
                </div>
              </div>
              {current === alias.name && <span className={CHIP_ACCENT}>{t('settings.ssh.active')}</span>}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// ════════════════════════════════════════════════════════════════════════════
// Usage tab — stats with range selector + trend + breakdowns
// ════════════════════════════════════════════════════════════════════════════

function UsageTab() {
  const { t, i18n } = useTranslation()
  const [stats, setStats] = useState<UsageStats | null>(null)
  const [days, setDays] = useState(30)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    setLoading(true)
    setError('')
    api
      .usageStats(days)
      .then(setStats)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false))
  }, [days])

  const fmtCompact = (n: number) => new Intl.NumberFormat(i18n.language, { notation: 'compact', maximumFractionDigits: 1 }).format(n)
  const fmtFull = (n: number) => new Intl.NumberFormat(i18n.language).format(n)
  const fmtPct = (frac: number) => `${Math.round(frac * 100)}%`

  if (loading) {
    return <div className="animate-pulse py-6 text-center text-xs text-[var(--color-muted-foreground)]">{t('common.loading')}</div>
  }
  if (!stats) {
    return <EmptyState Icon={ChartBarIcon} title={t('settings.usageStats.noData')} hint={error || t('settings.usageStats.subtitle')} />
  }

  const totals = stats.totals
  const trend = stats.daily_trend
  const maxTokens = Math.max(1, ...trend.map((d) => d.tokens))
  const heat = buildHeatmap(stats.heatmap)

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className={SECTION_TITLE}>{t('settings.usageStats.title')}</h3>
          <p className="mt-0.5 text-[11px] text-[var(--color-muted-foreground)]">{t('settings.usageStats.subtitle')}</p>
        </div>
        <div className="inline-flex rounded-[var(--radius-md)] bg-[var(--color-secondary)] p-0.5">
          {[7, 30].map((d) => (
            <button
              key={d}
              type="button"
              onClick={() => setDays(d)}
              className={`h-7 rounded-[var(--radius-sm)] px-2.5 text-xs transition-colors ${
                days === d
                  ? 'bg-[var(--color-background)] font-medium text-[var(--color-foreground)] shadow-[var(--shadow-sm)]'
                  : 'text-[var(--color-muted-foreground)] hover:text-[var(--color-foreground)]'
              }`}
            >
              {t('settings.usageStats.lastNDays').replace('{n}', String(d))}
            </button>
          ))}
        </div>
      </div>

      {error && <div className="rounded-[var(--radius-md)] bg-[var(--color-warning-bg)] px-3 py-2 text-xs text-[var(--color-warning-fg)]">{error}</div>}

      <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-3">
        <StatCard label={t('settings.usageStats.totalTokens')} value={fmtCompact(totals.total_tokens)} title={fmtFull(totals.total_tokens)} large />
        <StatCard label={t('settings.usageStats.cacheHitRate')} value={stats.cache_supported ? fmtPct(stats.cache_hit_rate) : '—'} large accent />
        <StatCard label={t('settings.usageStats.mostUsedModel')} value={stats.most_used_model || '—'} title={stats.most_used_model} large />
        <StatCard label={t('settings.usageStats.sessions')} value={fmtFull(totals.sessions)} />
        <StatCard label={t('settings.usageStats.turns')} value={fmtFull(totals.turns)} />
        <StatCard label={t('settings.usageStats.activeDays')} value={fmtFull(stats.active_days)} sub={t('settings.usageStats.streak').replace('{n}', String(stats.current_streak))} />
      </div>

      <section className={US_PANEL}>
        <div className={US_PANEL_TITLE}>{t('settings.usageStats.tokenBreakdown')}</div>
        <div className="mt-2 grid grid-cols-2 gap-2.5 sm:grid-cols-4">
          <MiniStat label={t('settings.usageStats.promptTokens')} value={fmtCompact(totals.prompt_tokens)} />
          <MiniStat label={t('settings.usageStats.cachedTokens')} value={fmtCompact(totals.cached_tokens)} />
          <MiniStat label={t('settings.usageStats.completionTokens')} value={fmtCompact(totals.completion_tokens)} />
          <MiniStat label={t('settings.usageStats.reasoningTokens')} value={fmtCompact(totals.reasoning_tokens)} />
        </div>
        <div className="mt-2 text-[10.5px] text-[var(--color-muted-foreground)]">{t('settings.usageStats.tokenBreakdownHint')}</div>
      </section>

      <section className={US_PANEL}>
        <div className="flex items-center justify-between">
          <div className={US_PANEL_TITLE}>{t('settings.usageStats.heatmap')}</div>
          <div className="flex items-center gap-1 text-[10px] text-[var(--color-muted-foreground)]">
            <span>{t('settings.usageStats.less')}</span>
            {HEAT_FILL.map((fill) => <span key={fill} className="inline-block h-2.5 w-2.5 rounded-[2px]" style={{ background: fill }} />)}
            <span>{t('settings.usageStats.more')}</span>
          </div>
        </div>
        <div className="mt-2 overflow-x-auto pb-1">
          <div className="grid w-max grid-flow-col grid-rows-7 gap-[3px]">
            {heat.map((cell) => (
              <span
                key={cell.date}
                title={cell.future ? '' : cell.tokens > 0 ? `${cell.date} · ${t('common.tokens', { used: fmtCompact(cell.tokens) })} · ${cell.turns} ${t('settings.usageStats.turnsUnit')}` : `${cell.date} · ${t('settings.usageStats.noActivity')}`}
                className="h-[11px] w-[11px] rounded-[2px]"
                style={{ background: cell.future ? 'transparent' : HEAT_FILL[cell.level] }}
              />
            ))}
          </div>
        </div>
      </section>

      <section className={US_PANEL}>
        <div className={US_PANEL_TITLE}>{t('settings.usageStats.dailyTrend')}</div>
        {trend.length > 0 ? (
          <div className="mt-3 flex h-[120px] items-end gap-[3px]">
            {trend.map((d) => (
              <div
                key={d.date}
                title={`${d.date} · ${t('chat.tokens', { used: fmtCompact(d.tokens) })} · ${d.turns} ${t('settings.usageStats.turnsUnit')}`}
                className="min-w-[2px] flex-1 rounded-t-[2px] bg-[var(--accent-fill)] transition-[height]"
                style={{ height: `${Math.max(2, (d.tokens / maxTokens) * 100)}%`, opacity: d.tokens === 0 ? 0.25 : 0.9 }}
              />
            ))}
          </div>
        ) : (
          <div className="py-6 text-center text-[11px] text-[var(--color-muted-foreground)]">{t('settings.usageStats.noData')}</div>
        )}
      </section>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Breakdown title={t('settings.usageStats.byModel')} shares={stats.by_model} compact={fmtCompact} />
        <Breakdown title={t('settings.usageStats.byProject')} shares={stats.by_project} compact={fmtCompact} project />
      </div>
    </div>
  )
}

function StatCard({ label, value, title, sub, large, accent }: { label: string; value: string; title?: string; sub?: string; large?: boolean; accent?: boolean }) {
  return (
    <div className={`rounded-[var(--radius-md)] bg-[var(--color-secondary)] px-3 py-2 ${large ? 'py-3' : ''}`}>
      <div className="text-[10px] uppercase tracking-wide text-[var(--color-muted-foreground)]">{label}</div>
      <div title={title} className={`mt-0.5 truncate font-mono font-semibold ${large ? 'text-[18px]' : 'text-[14px]'} ${accent ? 'text-[var(--color-primary)]' : 'text-[var(--color-foreground)]'}`}>{value}</div>
      {sub && <div className="mt-px text-[10.5px] text-[var(--color-muted-foreground)]">{sub}</div>}
    </div>
  )
}

function MiniStat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-[10px] uppercase tracking-[0.05em] text-[var(--color-muted-foreground)]">{label}</div>
      <div className="mt-0.5 font-mono text-sm font-semibold text-[var(--color-foreground)]">{value}</div>
    </div>
  )
}

function Breakdown({
  title,
  shares,
  compact,
  project,
}: {
  title: string
  shares: { name: string; tokens: number; share: number }[]
  compact: (n: number) => string
  project?: boolean
}) {
  const { t } = useTranslation()
  const sorted = [...shares].sort((a, b) => b.tokens - a.tokens).slice(0, 8)
  return (
    <section className={US_PANEL}>
      <div className={US_PANEL_TITLE}>{title}</div>
      <div className="space-y-1.5">
        {sorted.length === 0 && <div className="text-[11px] text-[var(--color-muted-foreground)]">{t('settings.usageStats.noData')}</div>}
        {sorted.map((s) => (
          <div key={s.name}>
            <div className="flex items-center justify-between text-[11px]">
              <span className="truncate text-[var(--color-foreground)]" title={s.name}>{project ? shortProjectName(s.name) : s.name}</span>
              <span className="ml-2 shrink-0 font-mono text-[var(--color-muted-foreground)]">
                {compact(s.tokens)}
              </span>
            </div>
            <div className="mt-0.5 h-1.5 overflow-hidden rounded-full bg-[var(--color-muted)]">
              <div
                className="h-full rounded-full"
                style={{ width: `${Math.max(1, s.share * 100)}%`, backgroundColor: 'var(--color-accent-neutral)' }}
              />
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}

const US_PANEL = 'rounded-[var(--radius-md)] bg-[var(--color-secondary)] p-3'
const US_PANEL_TITLE = 'text-[11px] font-semibold uppercase tracking-[0.06em] text-[var(--color-muted-foreground)]'
const HEAT_FILL = [
  'color-mix(in srgb, var(--color-foreground) 7%, transparent)',
  'color-mix(in srgb, var(--color-primary) 28%, transparent)',
  'color-mix(in srgb, var(--color-primary) 48%, transparent)',
  'color-mix(in srgb, var(--color-primary) 72%, transparent)',
  'var(--color-primary)',
]

function buildHeatmap(buckets: { date: string; tokens: number; turns: number }[]) {
  const map = new Map(buckets.map((b) => [b.date, b]))
  const max = Math.max(0, ...buckets.map((b) => b.tokens))
  const today = new Date()
  today.setHours(0, 0, 0, 0)
  const lastSunday = new Date(today)
  lastSunday.setDate(today.getDate() - today.getDay())
  const start = new Date(lastSunday)
  start.setDate(lastSunday.getDate() - 52 * 7)
  const cells: { date: string; tokens: number; turns: number; level: number; future: boolean }[] = []
  for (let w = 0; w < 53; w++) {
    for (let d = 0; d < 7; d++) {
      const cur = new Date(start)
      cur.setDate(start.getDate() + w * 7 + d)
      const key = dateKey(cur)
      const bucket = map.get(key)
      const tokens = bucket?.tokens || 0
      cells.push({
        date: key,
        tokens,
        turns: bucket?.turns || 0,
        level: levelFor(tokens, max),
        future: cur.getTime() > today.getTime(),
      })
    }
  }
  return cells
}

function levelFor(tokens: number, max: number): number {
  if (tokens <= 0 || max <= 0) return 0
  const ratio = Math.log(tokens + 1) / Math.log(max + 1)
  return Math.min(4, Math.max(1, Math.ceil(ratio * 4)))
}

function dateKey(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

function shortProjectName(path: string): string {
  const parts = path.replace(/\/+$/, '').split('/').filter(Boolean)
  return parts.at(-1) || path
}
