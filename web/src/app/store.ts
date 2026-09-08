/**
 * Redux Toolkit store — the React app's state layer.
 *
 * The Vue app had one 1209-line Pinia chat store. Here we split it across
 * focused slices (per the migration assessment), each owning a clear concern:
 *   - chat      : timeline + streaming accumulation
 *   - session   : sessions/tasks list, current session, project
 *   - approval  : approval gates + ask-user interactive blocks
 *   - model     : provider/model/mode/favorites
 *
 * The whole store is exposed to jcode-ui via createExternalStoreRuntime +
 * a select() that projects to RuntimeState (see app/runtime.ts).
 */

import { configureStore, createSlice, createAsyncThunk } from '@reduxjs/toolkit'
import type {
  ArtifactRef,
  ThreadItem,
  Message,
  ToolCall,
  Approval,
  TokenSnapshot,
  Goal,
  TodoItem,
  QueuedMessage,
  AskUserQuestion,
  AskUserAnswer,
} from 'jcode-ui-core'
import { api, isAPIError } from '../lib/api'
import { extractToolDisplayInfo } from '../lib/toolInfo'
import { normalizeMode, type AgentMode, type CustomAgentInfo, type ProviderInfo, type SessionItem, type TaskItem, type ProjectInfo, type SlashCommandInfo, type SessionEntry, type ModelRef, type RemoteConnectRequest, type RemoteHostKeyErrorPayload, type RemoteHostKeyErrorCode, type RemoteConnectionStatusData, type ModelRetryStatusData, type SessionActivationResponse, type WorkspaceKind } from '../lib/types'
import { parseRemoteLabel } from '../lib/remote'
import { i18n, setLocale, SUPPORTED_LOCALES } from '../i18n'
import { hydrateTheme } from '../lib/useTheme'
import { isTauri } from '../lib/useDesktop'
import { mergeToolLifecycle, normalizeWireLifecycle, settleIncompleteImageTool } from './toolLifecycle'

// ─── seq counter (stable DOM identity across streaming updates) ───
let _seq = 0
const nextSeq = () => ++_seq
function genId(prefix: string) {
  return `${prefix}_${Date.now()}_${Math.random().toString(36).slice(2, 6)}`
}

/** True when `candidate` is a strictly newer instant than `current` (either
 *  may be undefined). Compares PARSED instants, not raw strings: RFC3339
 *  string order breaks across UTC offsets ("05:00Z" < "13:00+08:00" though
 *  they're the same instant), and the data mixes server-local and UTC writes.
 *  An unparseable candidate never wins. */
function isNewerTs(candidate: string, current: string | undefined): boolean {
  const ct = Date.parse(candidate)
  if (Number.isNaN(ct)) return false
  if (!current) return true
  const cur = Date.parse(current)
  if (Number.isNaN(cur)) return true
  return ct > cur
}

interface ReplayGenerationOperation {
  operationID: string
  toolCallID?: string
  state: NonNullable<SessionEntry['operation_state']>
  artifactIDs: string[]
  errorCode?: string
  provider?: string
  model?: string
}

/** Rebuild the visible transcript from the durable JSONL contract. Operation
 * entries win over generic tool_result strings; a same-operation Artifact is
 * the next strongest proof of success. */
export function replayTimeline(entries: SessionEntry[], sessionRunning: boolean): ThreadItem[] {
  const artifactsByID = replayArtifacts(entries)
  const occurrenceIndex = replayOperationsByOccurrence(entries)
  const timeline: ThreadItem[] = []
  const pendingToolCalls = new Map<string, ToolCall>()
  const unresolvedToolCalls = new Set<ToolCall>()
  const toolsByOperationID = new Map<string, ToolCall>()

  for (let entryIndex = 0; entryIndex < entries.length; entryIndex++) {
    const e = entries[entryIndex]
    if (e.type === 'session_start' && e.agent) {
      timeline.push({
        kind: 'message',
        seq: nextSeq(),
        data: {
          id: genId('agent'),
          role: 'system',
          content: i18n.t('chat.agent.changedTo', { name: e.agent }),
          level: 'notice',
          timestamp: ts(e.timestamp),
        },
      })
    } else if (e.type === 'user' && (e.content || (e.images && e.images.length > 0))) {
      timeline.push({ kind: 'message', seq: nextSeq(), data: { id: genId('msg'), role: 'user', content: e.content || '', timestamp: ts(e.timestamp), images: e.images } })
    } else if (e.type === 'assistant' && e.content) {
      timeline.push({ kind: 'message', seq: nextSeq(), data: { id: genId('asst'), role: 'assistant', content: e.content, timestamp: ts(e.timestamp) } })
    } else if (e.type === 'agent_change') {
      timeline.push({
        kind: 'message',
        seq: nextSeq(),
        data: {
          id: genId('agent'),
          role: 'system',
          content: e.agent
            ? i18n.t('chat.agent.changedTo', { name: e.agent })
            : i18n.t('chat.agent.changedToDefault'),
          level: 'notice',
          timestamp: ts(e.timestamp),
        },
      })
    } else if (e.type === 'tool_call' && e.name) {
      const operation = occurrenceIndex.operations.get(entryIndex)
      const tc: ToolCall = {
        id: genId('tc'),
        toolCallID: e.tool_call_id,
        operationID: operation?.operationID || occurrenceIndex.operationIDs.get(entryIndex),
        name: e.name,
        args: e.args || '',
        status: 'running',
        surface: e.name === 'generate_image' ? 'standalone' : undefined,
        phase: e.name === 'generate_image' ? 'queued' : undefined,
        timestamp: ts(e.timestamp),
        displayInfo: extractToolDisplayInfo(e.name, e.args || ''),
        batchId: e.batch_id,
        batchIndex: e.batch_index,
        batchSize: e.batch_size,
        startedAt: e.started_at,
      }
      if (e.name === 'generate_image') reconcileReplayImage(tc, operation, artifactsByID)
      timeline.push({ kind: 'tool', seq: nextSeq(), data: tc })
      if (e.tool_call_id) pendingToolCalls.set(e.tool_call_id, tc)
      unresolvedToolCalls.add(tc)
      if (tc.operationID) toolsByOperationID.set(tc.operationID, tc)
    } else if (e.type === 'tool_result') {
      const tc = findReplayTool(e, pendingToolCalls, toolsByOperationID, timeline)
      if (!tc) continue
      if (e.operation_id && !toolsByOperationID.has(e.operation_id) && (!tc.operationID || tc.operationID === e.operation_id)) {
        tc.operationID = e.operation_id
        toolsByOperationID.set(e.operation_id, tc)
      }
      applyReplayToolResult(tc, e, artifactsByID)
      unresolvedToolCalls.delete(tc)
      if (e.tool_call_id && pendingToolCalls.get(e.tool_call_id) === tc) {
        pendingToolCalls.delete(e.tool_call_id)
      }
    }
  }

  for (const tc of unresolvedToolCalls) {
    if (tc.name === 'generate_image') {
      if (!sessionRunning) settleIncompleteImageTool(tc)
    } else {
      tc.status = 'done'
    }
  }
  return timeline
}

function replayArtifacts(entries: SessionEntry[]): Map<string, ArtifactRef> {
  const records = new Map<string, { revision: number; ref: ArtifactRef }>()
  for (const e of entries) {
    if (e.type !== 'artifact' || !e.artifact_id) continue
    const revision = e.artifact_revision ?? 0
    if ((records.get(e.artifact_id)?.revision ?? -1) > revision) continue
    records.set(e.artifact_id, {
      revision,
      ref: {
        id: e.artifact_id,
        storage: e.artifact_storage_kind === 'managed' ? 'managed' : 'workspace',
        key: e.artifact_key || e.artifact_path || '',
        title: e.artifact_title || 'Generated image',
        kind: e.artifact_kind || 'image',
        media_type: e.artifact_media_type || 'application/octet-stream',
        size: e.artifact_size ?? 0,
        width: e.artifact_width || undefined,
        height: e.artifact_height || undefined,
        provider: e.artifact_provider_id || undefined,
        model: e.artifact_model_id || undefined,
        operation_id: e.operation_id || undefined,
        tool_call_id: e.tool_call_id || undefined,
        shareable: e.artifact_shareable || undefined,
      },
    })
  }
  return new Map([...records].map(([id, record]) => [id, record.ref]))
}

interface ReplayToolOccurrence {
  entryIndex: number
  name: string
  toolCallID: string
  operationID?: string
  operation?: ReplayGenerationOperation
}

interface ReplayOccurrenceIndex {
  operations: Map<number, ReplayGenerationOperation>
  operationIDs: Map<number, string>
}

/** Bind durable generation evidence to one concrete tool-call occurrence.
 *
 * Tool-call IDs come from the model and are not guaranteed to be unique across
 * turns. A session-wide `tool_call_id -> operation` map therefore lets a later
 * turn rewrite an earlier card. The JSONL append order gives us a stronger
 * contract: a new tool_call opens an occurrence, its matching tool_result (or
 * another call reusing the ID) closes that interval, and the first operation
 * observed inside the interval permanently binds its opaque operation ID to
 * that occurrence. Later journal transitions may then find the same host by
 * operation ID even after its tool_result was appended. */
function replayOperationsByOccurrence(entries: SessionEntry[]): ReplayOccurrenceIndex {
  const occurrences: ReplayToolOccurrence[] = []
  const openByToolCall = new Map<string, ReplayToolOccurrence>()
  const byOperationID = new Map<string, ReplayToolOccurrence>()

  for (let entryIndex = 0; entryIndex < entries.length; entryIndex++) {
    const e = entries[entryIndex]
    if (e.type === 'tool_call' && e.name === 'generate_image' && e.tool_call_id) {
      const occurrence: ReplayToolOccurrence = {
        entryIndex,
        name: e.name,
        toolCallID: e.tool_call_id,
      }
      occurrences.push(occurrence)
      // Reuse without a result still starts a new occurrence. Do not let later
      // evidence leak back into the abandoned interval.
      openByToolCall.set(e.tool_call_id, occurrence)
      continue
    }
    if (e.type === 'tool_result' && e.tool_call_id) {
      const open = openByToolCall.get(e.tool_call_id)
      const matchesOpenName = !!open && (!e.name || e.name === open.name)
      let operationHost = e.operation_id ? byOperationID.get(e.operation_id) : undefined
      if (open && matchesOpenName && e.operation_id && !operationHost && !open.operationID) {
        // A denied/pre-dispatch result can be the first durable record carrying
        // an operation identity. Bind it before closing the interval so a
        // duplicate late result cannot fall through to a later reused call ID.
        open.operationID = e.operation_id
        byOperationID.set(e.operation_id, open)
        operationHost = open
      }
      if (
        open &&
        matchesOpenName &&
        (!operationHost || operationHost === open) &&
        (!e.operation_id || !open.operationID || e.operation_id === open.operationID)
      ) {
        openByToolCall.delete(e.tool_call_id)
      }
      continue
    }
    if (e.type !== 'generation_operation' || !e.operation_id || !e.tool_call_id || !e.operation_state) continue

    let occurrence = byOperationID.get(e.operation_id)
    if (!occurrence) {
      occurrence = openByToolCall.get(e.tool_call_id)
      // The first operation is the immutable host identity for this occurrence.
      // A second operation ID inside the same interval is corrupt/ambiguous and
      // must not replace it.
      if (!occurrence || (occurrence.operationID && occurrence.operationID !== e.operation_id)) continue
      occurrence.operationID = e.operation_id
      byOperationID.set(e.operation_id, occurrence)
    }
    if (occurrence.toolCallID !== e.tool_call_id || occurrence.operationID !== e.operation_id) continue

    const candidate: ReplayGenerationOperation = {
      operationID: e.operation_id,
      toolCallID: e.tool_call_id,
      state: e.operation_state,
      artifactIDs: e.artifact_ids ?? [],
      errorCode: e.error_code,
      provider: e.operation_capability_key?.provider_profile_id,
      model: e.operation_capability_key?.model_id,
    }
    const current = occurrence.operation
    if (!current || operationRank(candidate.state) >= operationRank(current.state)) {
      occurrence.operation = candidate
    }
  }

  const byEntryIndex = new Map<number, ReplayGenerationOperation>()
  const operationIDs = new Map<number, string>()
  for (const occurrence of occurrences) {
    if (occurrence.operation) byEntryIndex.set(occurrence.entryIndex, occurrence.operation)
    if (occurrence.operationID) operationIDs.set(occurrence.entryIndex, occurrence.operationID)
  }
  return { operations: byEntryIndex, operationIDs }
}

function operationRank(state: ReplayGenerationOperation['state']): number {
  if (state === 'succeeded' || state === 'failed' || state === 'uncertain') return 4
  if (state === 'saving') return 3
  if (state === 'accepted') return 2
  return 1
}

function reconcileReplayImage(
  tool: ToolCall,
  operation: ReplayGenerationOperation | undefined,
  artifactsByID: Map<string, ArtifactRef>,
): void {
  if (!operation) return
  const artifacts = [...artifactsByID.values()].filter((artifact) =>
    operation.artifactIDs.includes(artifact.id) || artifact.operation_id === operation.operationID,
  )
  if (operation.state === 'succeeded' || operation.state === 'failed' || operation.state === 'uncertain') {
    mergeToolLifecycle(tool, {
      operationID: operation.operationID,
      phase: 'terminal',
      outcome: operation.state,
      errorCode: operation.errorCode,
      provider: operation.provider,
      model: operation.model,
      artifacts,
    })
    return
  }
  if (artifacts.length > 0) {
    mergeToolLifecycle(tool, {
      operationID: operation.operationID,
      phase: 'terminal',
      outcome: 'succeeded',
      provider: operation.provider || artifacts[0]?.provider,
      model: operation.model || artifacts[0]?.model,
      artifacts,
    })
    return
  }
  // Keep a non-terminal journal state non-terminal until the whole replay has
  // been scanned. A later terminal tool_result outranks it; only an unmatched
  // historical call is settled to uncertain in the final pending pass.
  mergeToolLifecycle(tool, {
    operationID: operation.operationID,
    phase: operation.state === 'saving' ? 'saving' : 'generating',
    errorCode: operation.errorCode,
    provider: operation.provider,
    model: operation.model,
  })
}

function findReplayTool(
  e: SessionEntry,
  pending: Map<string, ToolCall>,
  byOperationID: Map<string, ToolCall>,
  timeline: ThreadItem[],
): ToolCall | undefined {
  if (e.operation_id) {
    const exact = byOperationID.get(e.operation_id)
    if (exact && (!e.tool_call_id || exact.toolCallID === e.tool_call_id)) return exact
    if (e.tool_call_id) {
      const open = pending.get(e.tool_call_id)
      if (open && (!open.operationID || open.operationID === e.operation_id)) return open
      return undefined
    }
  }
  if (e.tool_call_id) return pending.get(e.tool_call_id)
  for (let i = timeline.length - 1; i >= 0; i--) {
    const item = timeline[i]
    if (item.kind === 'tool' && item.data.name === e.name && item.data.status === 'running') return item.data
  }
  return undefined
}

function applyReplayToolResult(tool: ToolCall, e: SessionEntry, artifactsByID: Map<string, ArtifactRef>): void {
  tool.output = e.output || ''
  tool.error = e.error || ''
  tool.denied = e.denied || undefined
  if (e.duration_ms !== undefined && tool.meta?.duration_ms === undefined) {
    tool.meta = { ...(tool.meta || {}), duration_ms: e.duration_ms }
  }
  if (tool.name !== 'generate_image') {
    tool.status = e.error ? 'error' : 'done'
    return
  }
  // A terminal journal state already won during the first replay pass.
  if (tool.phase === 'terminal') return
  const artifacts = (e.artifact_ids ?? []).map((id) => artifactsByID.get(id)).filter((artifact): artifact is ArtifactRef => !!artifact)
  const wire = normalizeWireLifecycle(e.outcome as Parameters<typeof normalizeWireLifecycle>[0] | undefined)
  const outcome = wire.outcome ?? (e.denied ? 'cancelled' : artifacts.length > 0 ? 'succeeded' : e.error ? 'failed' : 'uncertain')
  mergeToolLifecycle(tool, {
    operationID: e.operation_id,
    phase: 'terminal',
    outcome,
    errorCode: e.error_code || (e.error ? 'legacy_tool_error' : undefined),
    provider: e.provider,
    model: e.model,
    artifacts,
  })
}

function lifecycleHostIndex(
  timeline: readonly ThreadItem[],
  toolCallID: string,
  operationID?: string,
  name?: string,
): number {
  // Once an operation ID exists it is the strongest host identity, including
  // for late/duplicate events that arrive after the occurrence is terminal.
  if (operationID) {
    for (let i = timeline.length - 1; i >= 0; i--) {
      const item = timeline[i]
      if (
        item.kind === 'tool' &&
        item.data.toolCallID === toolCallID &&
        item.data.operationID === operationID &&
        (!name || item.data.name === name)
      ) return i
    }
  }

  // An operation's first lifecycle event may bind only the latest live,
  // unbound occurrence. A terminal historical card that merely reused the
  // model-supplied call ID is never a host for a new operation.
  for (let i = timeline.length - 1; i >= 0; i--) {
    const item = timeline[i]
    if (
      item.kind === 'tool' &&
      item.data.toolCallID === toolCallID &&
      (!name || item.data.name === name) &&
      item.data.status === 'running' &&
      item.data.phase !== 'terminal' &&
      (!operationID || !item.data.operationID || item.data.operationID === operationID)
    ) return i
  }
  return -1
}

/** True only when a typed lifecycle event has a concrete occurrence to
 * update. Used by the WS bridge to distinguish a dropped initial tool_call
 * frame from a duplicate event for an older operation. */
export function hasToolLifecycleHost(
  timeline: readonly ThreadItem[],
  toolCallID: string,
  operationID?: string,
  name?: string,
): boolean {
  return lifecycleHostIndex(timeline, toolCallID, operationID, name) >= 0
}

interface ResolvedToolFields {
  name: string
  output?: string
  displayOutput?: string
  error?: string
  denied?: boolean
  durationMs?: number
  streams?: ToolCall['streams']
  meta?: ToolCall['meta']
  presentation?: ToolCall['presentation']
}

function applyResolvedToolFields(tool: ToolCall, fields: ResolvedToolFields): void {
  tool.output = fields.output
  tool.displayOutput = fields.displayOutput
  tool.error = fields.error
  // Denied (user rejection) is a distinct state from error; a result always
  // clears the awaiting-approval highlight.
  tool.denied = fields.denied || undefined
  tool.awaitingApproval = undefined
  // A finished ask_user is no longer interactive — drop the pending markers so
  // the docked card cannot reappear if a late tool_result races the optimistic
  // resolve path.
  if (tool.name === 'ask_user' || fields.name === 'ask_user') {
    tool.askUserId = undefined
    tool.askUserQuestions = undefined
    delete (tool as ToolCall & { askUserTaskId?: string }).askUserTaskId
  }
  if (fields.streams) tool.streams = fields.streams
  if (fields.meta) tool.meta = fields.meta
  // Merge the event-level duration into meta when meta lacks one (falling back
  // to the local startedAt delta) so finished rows show an accurate duration.
  const duration =
    tool.meta?.duration_ms ??
    fields.durationMs ??
    (tool.startedAt ? Date.now() - tool.startedAt : undefined)
  if (duration !== undefined) tool.meta = { ...(tool.meta || {}), duration_ms: duration }
  if (fields.presentation) {
    tool.presentation = fields.presentation
    tool.displayInfo = {
      ...(tool.displayInfo || { title: fields.name }),
      kind: fields.presentation.kind || tool.displayInfo?.kind,
      collapsible: fields.presentation.collapsible ?? tool.displayInfo?.collapsible,
    }
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// chat slice — timeline + streaming accumulation
// ═══════════════════════════════════════════════════════════════════════════

interface ChatState {
  timeline: ThreadItem[]
  isRunning: boolean
  /** A session resume (replay) is in flight — the chat pane shows a skeleton
   *  so the switch feels instant instead of blank-until-ready. */
  sessionLoading: boolean
  tokenSnapshot: TokenSnapshot | null
  goal: Goal | null
  goalArmed: boolean
  todos: TodoItem[]
  /** Type-ahead queues keyed by session id — a message queued while an agent
   *  runs belongs to THAT conversation and must survive switching away and
   *  back (previously a single global list wiped by clearChat on switch). */
  queuedBySession: Record<string, QueuedMessage[]>
  slashCommands: SlashCommandInfo[]
}

const initialChat: ChatState = {
  timeline: [],
  isRunning: false,
  sessionLoading: false,
  tokenSnapshot: null,
  goal: null,
  goalArmed: false,
  todos: [],
  queuedBySession: {},
  slashCommands: [],
}

// Streaming accumulators — non-reactive module state (matches the Vue pattern).
let streamingText = ''
let streamingMsgId = ''

const chatSlice = createSlice({
  name: 'chat',
  initialState: initialChat,
  reducers: {
    clearChat(s) {
      s.timeline = []
      s.isRunning = false
      s.tokenSnapshot = null
      s.goal = null
      s.goalArmed = false
      s.todos = []
      // NOTE: queuedBySession is deliberately NOT cleared here — clearChat runs
      // on every session switch, and stashed type-ahead queues must survive.
      streamingText = ''
      streamingMsgId = ''
    },
    setRunning(s, a: { payload: boolean }) {
      s.isRunning = a.payload
    },
    setSessionLoading(s, a: { payload: boolean }) {
      s.sessionLoading = a.payload
    },
    addMessage(
      s,
      a: { payload: { role: Message['role']; content: string; source?: string; images?: Message['images']; level?: Message['level']; detail?: string; durationMs?: number } },
    ) {
      const msg: Message = {
        id: genId('msg'),
        timestamp: Date.now(),
        ...a.payload,
      }
      s.timeline.push({ kind: 'message', data: msg, seq: nextSeq() })
    },
    appendAgentText(s, a: { payload: string }) {
      const delta = a.payload
      streamingText += delta
      if (!streamingMsgId) {
        const msg: Message = {
          id: genId('asst'),
          role: 'assistant',
          content: streamingText,
          timestamp: Date.now(),
        }
        streamingMsgId = msg.id
        s.timeline.push({ kind: 'message', data: msg, seq: nextSeq() })
      } else {
        const item = s.timeline.find((i) => i.kind === 'message' && i.data.id === streamingMsgId)
        if (item && item.kind === 'message') item.data.content = streamingText
      }
    },
    addToolCall(
      s,
      a: {
        payload: {
          name: string
          args: string
          toolCallID?: string
          displayInfo?: ToolCall['displayInfo']
          batchId?: string
          batchIndex?: number
          batchSize?: number
          startedAt?: number
          surface?: ToolCall['surface']
          phase?: ToolCall['phase']
          operationID?: string
          approvalID?: string
          approvalGranted?: boolean
        }
      },
    ) {
      // A tool call flushes the streaming text accumulator (text after a tool
      // starts a fresh assistant message).
      streamingText = ''
      streamingMsgId = ''
      if (a.payload.approvalID) {
        // Approval IDs are minted by the host for one concrete gate. Call IDs
        // are model supplied and may repeat even inside one turn, so never use
        // them alone to settle an approval.
        for (let i = s.timeline.length - 1; i >= 0; i--) {
          const item = s.timeline[i]
          if (item.kind === 'message' && item.data.role === 'user') break
          if (
            item.kind !== 'approval' ||
            item.data.id !== a.payload.approvalID ||
            (a.payload.toolCallID && item.data.tool_call_id !== a.payload.toolCallID) ||
            item.data.tool_name !== a.payload.name ||
            item.data.resolved
          ) continue
          item.data.resolved = true
          item.data.approved = a.payload.approvalGranted === true
          item.data.resolving = false
          break
        }
      }
      // A pending-approval reload reconstructs the proposed tool from JSONL,
      // then the Web handler releases the SAME tool_call only after Allow. Fold
      // that release into the current turn's unresolved occurrence instead of
      // appending a duplicate card. Stop at the latest user boundary because
      // model-supplied call IDs may be reused across turns.
      if (a.payload.toolCallID) {
        for (let i = s.timeline.length - 1; i >= 0; i--) {
          const item = s.timeline[i]
          if (item.kind === 'message' && item.data.role === 'user') break
          if (
            item.kind !== 'tool' ||
            item.data.toolCallID !== a.payload.toolCallID ||
            item.data.name !== a.payload.name ||
            (a.payload.args !== '' && item.data.args !== a.payload.args) ||
            item.data.output !== undefined ||
            item.data.error !== undefined ||
            item.data.phase === 'terminal'
          ) continue
          item.data.args = a.payload.args || item.data.args
          item.data.status = 'running'
          item.data.displayInfo = a.payload.displayInfo ?? item.data.displayInfo
          item.data.batchId = a.payload.batchId ?? item.data.batchId
          item.data.batchIndex = a.payload.batchIndex ?? item.data.batchIndex
          item.data.batchSize = a.payload.batchSize ?? item.data.batchSize
          item.data.startedAt = a.payload.startedAt ?? Date.now()
          item.data.surface = a.payload.surface ?? item.data.surface
          item.data.phase = a.payload.phase ?? item.data.phase
          item.data.operationID = a.payload.operationID ?? item.data.operationID
          item.data.approvalID = a.payload.approvalID ?? item.data.approvalID
          return
        }
      }
      // ask_user_request can arrive before the matching tool_call and already
      // insert a pending row. Fold this event into that row so the dock and
      // timeline do not each render their own card.
      if (a.payload.name === 'ask_user') {
        for (let i = s.timeline.length - 1; i >= 0; i--) {
          const item = s.timeline[i]
          if (item.kind !== 'tool' || item.data.name !== 'ask_user') continue
          if (item.data.status !== 'running' || item.data.output) continue
          if (item.data.toolCallID && a.payload.toolCallID && item.data.toolCallID !== a.payload.toolCallID) continue
          item.data.toolCallID = a.payload.toolCallID ?? item.data.toolCallID
          if (a.payload.args) item.data.args = a.payload.args
          item.data.displayInfo = a.payload.displayInfo ?? item.data.displayInfo
          item.data.batchId = a.payload.batchId ?? item.data.batchId
          item.data.batchIndex = a.payload.batchIndex ?? item.data.batchIndex
          item.data.batchSize = a.payload.batchSize ?? item.data.batchSize
          item.data.startedAt = a.payload.startedAt ?? item.data.startedAt
          item.data.surface = a.payload.surface ?? item.data.surface
          item.data.phase = a.payload.phase ?? item.data.phase
          item.data.operationID = a.payload.operationID ?? item.data.operationID
          item.data.approvalID = a.payload.approvalID ?? item.data.approvalID
          return
        }
      }
      const tc: ToolCall = {
        id: genId('tool'),
        name: a.payload.name,
        args: a.payload.args,
        toolCallID: a.payload.toolCallID,
        status: 'running',
        timestamp: Date.now(),
        displayInfo: a.payload.displayInfo,
        batchId: a.payload.batchId,
        batchIndex: a.payload.batchIndex,
        batchSize: a.payload.batchSize,
        // Fallback to the local clock so the live elapsed badge always works.
        startedAt: a.payload.startedAt ?? Date.now(),
        surface: a.payload.surface ?? (a.payload.name === 'generate_image' ? 'standalone' : undefined),
        phase: a.payload.phase ?? (a.payload.name === 'generate_image' ? 'queued' : undefined),
        operationID: a.payload.operationID,
        approvalID: a.payload.approvalID,
      }
      s.timeline.push({ kind: 'tool', data: tc, seq: nextSeq() })
    },
    progressToolCall(
      s,
      a: { payload: { name?: string; toolCallID: string; operationID?: string; phase?: ToolCall['phase']; outcome?: ToolCall['outcome']; errorCode?: string; provider?: string; model?: string; artifacts?: ArtifactRef[] } },
    ) {
      const index = lifecycleHostIndex(
        s.timeline,
        a.payload.toolCallID,
        a.payload.operationID,
        a.payload.name,
      )
      if (index < 0) return
      const item = s.timeline[index]
      if (item.kind !== 'tool') return
      mergeToolLifecycle(item.data, {
        operationID: a.payload.operationID,
        phase: a.payload.phase,
        outcome: a.payload.outcome,
        errorCode: a.payload.errorCode,
        provider: a.payload.provider,
        model: a.payload.model,
        artifacts: a.payload.artifacts,
      })
    },
    resolveToolCall(
      s,
      a: {
        payload: {
          name: string
          toolCallID?: string
          approvalID?: string
          output?: string
          displayOutput?: string
          error?: string
          denied?: boolean
          durationMs?: number
          streams?: ToolCall['streams']
          meta?: ToolCall['meta']
          presentation?: ToolCall['presentation']
          operationID?: string
          phase?: ToolCall['phase']
          outcome?: ToolCall['outcome']
          errorCode?: string
          provider?: string
          model?: string
          artifacts?: ArtifactRef[]
        }
      },
    ) {
      const {
        toolCallID,
        approvalID,
        name,
        output,
        displayOutput,
        error,
        denied,
        durationMs,
        streams,
        meta,
        presentation,
        operationID,
        phase,
        outcome,
        errorCode,
        provider,
        model,
        artifacts,
      } = a.payload
      const typedLifecycle = name === 'generate_image' || !!operationID || !!outcome || !!artifacts?.length
      if (toolCallID && typedLifecycle) {
        const index = lifecycleHostIndex(s.timeline, toolCallID, operationID, name)
        if (index < 0) return
        const item = s.timeline[index]
        if (item.kind !== 'tool') return
        const resolvedOutcome = outcome ?? (
          denied ? 'cancelled' : artifacts?.length ? 'succeeded' : error ? 'failed' : 'uncertain'
        )
        const merged = mergeToolLifecycle(item.data, {
          operationID,
          phase: phase ?? 'terminal',
          outcome: resolvedOutcome,
          errorCode,
          provider,
          model,
          artifacts,
        })
        if (merged === 'operation_mismatch' || merged === 'stale') return
        applyResolvedToolFields(item.data, {
          name, output, displayOutput, error, denied, durationMs, streams, meta, presentation,
        })
        return
      }
      let target = -1
      if (approvalID) {
        for (let i = s.timeline.length - 1; i >= 0; i--) {
          const item = s.timeline[i]
          if (item.kind === 'message' && item.data.role === 'user') break
          if (item.kind === 'tool' && item.data.approvalID === approvalID) {
            target = i
            break
          }
        }
      }
      if (target < 0 && toolCallID) {
        // Model IDs can be malformed/reused. Never overwrite a terminal card,
        // and fail closed when more than one unsettled occurrence is plausible.
        // Replay historically marks a call-without-result as done, so no-result
        // done rows remain eligible after a page refresh.
        for (let i = s.timeline.length - 1; i >= 0; i--) {
          const item = s.timeline[i]
          if (item.kind === 'message' && item.data.role === 'user') break
          const unsettled = item.kind === 'tool' && (
            item.data.status === 'running' || (
              item.data.status === 'done' && item.data.output === undefined &&
              item.data.error === undefined && item.data.phase !== 'terminal' &&
              item.data.meta?.exit_code === undefined && !item.data.denied
            )
          )
          if (
            item.kind !== 'tool' || item.data.toolCallID !== toolCallID ||
            item.data.name !== name || !unsettled
          ) continue
          if (target >= 0) {
            target = -1
            break
          }
          target = i
        }
      }
      if (target < 0 && !toolCallID) {
        for (let i = s.timeline.length - 1; i >= 0; i--) {
          const item = s.timeline[i]
          if (item.kind === 'message' && item.data.role === 'user') break
          if (item.kind === 'tool' && item.data.name === name && item.data.status === 'running') {
            target = i
            break
          }
        }
      }
      if (target >= 0) {
        const item = s.timeline[target]
        if (item.kind !== 'tool') return
        item.data.status = error ? 'error' : (meta?.exit_code !== undefined && meta.exit_code !== 0 ? 'error' : 'done')
        applyResolvedToolFields(item.data, {
          name, output, displayOutput, error, denied, durationMs, streams, meta, presentation,
        })
      }
    },
    setTokenSnapshot(s, a: { payload: TokenSnapshot | null }) {
      s.tokenSnapshot = a.payload
    },
    setGoal(s, a: { payload: Goal | null }) {
      s.goal = a.payload
    },
    setGoalArmed(s, a: { payload: boolean }) {
      s.goalArmed = a.payload
    },
    setTodos(s, a: { payload: TodoItem[] }) {
      s.todos = a.payload
    },
    enqueueMessage(s, a: { payload: { sessionId: string; message: QueuedMessage } }) {
      ;(s.queuedBySession[a.payload.sessionId] ??= []).push(a.payload.message)
    },
    removeQueued(s, a: { payload: { sessionId: string; id: string } }) {
      const q = s.queuedBySession[a.payload.sessionId]
      if (!q) return
      const next = q.filter((m) => m.id !== a.payload.id)
      if (next.length > 0) s.queuedBySession[a.payload.sessionId] = next
      else delete s.queuedBySession[a.payload.sessionId]
    },
    shiftQueued(s, a: { payload: string }) {
      // Pops the first queued message of the given session — the WS bridge
      // resends it on that session's agentDone.
      const q = s.queuedBySession[a.payload]
      if (!q) return
      q.shift()
      if (q.length === 0) delete s.queuedBySession[a.payload]
    },
    dropSessionQueue(s, a: { payload: string }) {
      // Session was deleted — its stash can never drain again.
      delete s.queuedBySession[a.payload]
    },
    agentDone(s, a: { payload: { error?: string; detail?: string; stopped?: boolean } | undefined }) {
      // Stamp wall-clock turn duration (user submit -> agent_done) on this
      // turn's final assistant message. Never fall back across the latest user
      // boundary: a failed/tool-only turn must not rewrite the previous turn.
      let userIndex = -1
      for (let i = s.timeline.length - 1; i >= 0; i--) {
        const item = s.timeline[i]
        if (item.kind === 'message' && item.data.role === 'user') {
          userIndex = i
          break
        }
      }
      if (userIndex >= 0) {
        const user = s.timeline[userIndex]
        for (let i = s.timeline.length - 1; i > userIndex; i--) {
          const item = s.timeline[i]
          if (item.kind === 'message' && item.data.role === 'assistant') {
            const startedAt = user.kind === 'message' ? user.data.timestamp : Date.now()
            item.data.durationMs = Math.max(0, Date.now() - startedAt)
            break
          }
        }
      }
      // Generic tools keep their historical done fallback. Image operations
      // require a typed terminal outcome and never become implicit successes.
      for (const item of s.timeline) {
        if (item.kind === 'tool' && item.data.status === 'running') {
          if (item.data.name === 'generate_image') settleIncompleteImageTool(item.data)
          else item.data.status = 'done'
        }
        if (item.kind === 'tool' && item.data.awaitingApproval) {
          item.data.awaitingApproval = undefined
        }
      }
      // A manual stop (a.payload?.stopped) intentionally adds nothing to the
      // timeline — the streaming indicator disappearing is enough feedback; a
      // "Stopped by user" system card felt like noise.
      if (a.payload?.error) {
        s.timeline.push({
          kind: 'message',
          data: {
            id: genId('sys'),
            role: 'system',
            content: a.payload.error,
            detail: a.payload.detail || undefined,
            timestamp: Date.now(),
            level: 'error',
          },
          seq: nextSeq(),
        })
      }
      s.isRunning = false
      streamingText = ''
      streamingMsgId = ''
    },
    setSlashCommands(s, a: { payload: SlashCommandInfo[] }) {
      s.slashCommands = a.payload
    },
    setTimeline(s, a: { payload: ThreadItem[] }) {
      s.timeline = a.payload
    },
    truncateTimelineFrom(s, a: { payload: string }) {
      const idx = s.timeline.findIndex((i) => i.kind === 'message' && i.data.id === a.payload)
      if (idx >= 0) s.timeline = s.timeline.slice(0, idx)
      streamingText = ''
      streamingMsgId = ''
    },
    addApprovalRequest(s, a: { payload: Approval }) {
      s.timeline.push({ kind: 'approval', data: a.payload, seq: nextSeq() })
      // Paint the gated tool row as awaiting approval (warning color) while
      // the prompt is unresolved.
      if (a.payload.tool_call_id) {
        for (let i = s.timeline.length - 1; i >= 0; i--) {
          const item = s.timeline[i]
          if (item.kind === 'tool' && item.data.toolCallID === a.payload.tool_call_id) {
            if (item.data.status === 'running') item.data.awaitingApproval = true
            break
          }
        }
      }
    },
    attachAskUser(s, a: { payload: { toolName: string; askUserId: string; questions: AskUserQuestion[]; taskId?: string } }) {
      // Arm the matching tool with ask_user state (the tool was added by tool_call).
      for (let i = s.timeline.length - 1; i >= 0; i--) {
        const item = s.timeline[i]
        if (item.kind !== 'tool' || item.data.name !== a.payload.toolName) continue
        if (item.data.askUserId === a.payload.askUserId) return
        if (!item.data.askUserId && (item.data.status === 'running' || (item.data.status === 'done' && !item.data.output))) {
          item.data.status = 'running'
          item.data.askUserId = a.payload.askUserId
          item.data.askUserQuestions = a.payload.questions
          ;(item.data as ToolCall & { askUserTaskId?: string }).askUserTaskId = a.payload.taskId
          return
        }
      }
      const args = JSON.stringify({ questions: a.payload.questions })
      const tc: ToolCall = {
        id: genId('ask'),
        name: a.payload.toolName,
        args,
        status: 'running',
        timestamp: Date.now(),
        displayInfo: extractToolDisplayInfo(a.payload.toolName, args),
        askUserId: a.payload.askUserId,
        askUserQuestions: a.payload.questions,
      }
      ;(tc as ToolCall & { askUserTaskId?: string }).askUserTaskId = a.payload.taskId
      s.timeline.push({ kind: 'tool', data: tc, seq: nextSeq() })
    },
    addSubagentProgress(s, a: { payload: { event: string; toolName: string; detail: string } }) {
      for (let i = s.timeline.length - 1; i >= 0; i--) {
        const item = s.timeline[i]
        if (item.kind !== 'tool' || item.data.name !== 'subagent' || item.data.status !== 'running') continue
        item.data.children ??= []
        if (a.payload.event === 'tool_call') {
          item.data.children.push({
            id: genId('sub_tc'),
            name: a.payload.toolName,
            args: a.payload.detail,
            status: 'running',
            timestamp: Date.now(),
            displayInfo: extractToolDisplayInfo(a.payload.toolName, a.payload.detail),
          })
        } else if (a.payload.event === 'tool_result') {
          for (let j = item.data.children.length - 1; j >= 0; j--) {
            const child = item.data.children[j]
            if (child.name === a.payload.toolName && child.status === 'running') {
              child.output = a.payload.detail
              child.status = 'done'
              break
            }
          }
        }
        break
      }
    },
    setApprovalResolving(s, a: { payload: { id: string; resolving: boolean } }) {
      const item = s.timeline.find((i) => i.kind === 'approval' && i.data.id === a.payload.id)
      if (item && item.kind === 'approval') item.data.resolving = a.payload.resolving
    },
    resolveApprovalItem(s, a: { payload: { id: string; approved: boolean; optionId?: string } }) {
      const item = s.timeline.find((i) => i.kind === 'approval' && i.data.id === a.payload.id)
      if (item && item.kind === 'approval') {
        item.data.resolved = true
        item.data.approved = a.payload.approved
        item.data.resolvedOptionId = a.payload.optionId
        item.data.resolving = false
        // Clear the awaiting-approval highlight on the gated tool row (the
        // denied strikethrough, if any, arrives with the tool_result).
        if (item.data.tool_call_id) {
          for (const t of s.timeline) {
            if (t.kind === 'tool' && t.data.toolCallID === item.data.tool_call_id) {
              t.data.awaitingApproval = undefined
              break
            }
          }
        }
      }
    },
    // Optimistically settle the docked ask_user card the moment /api/ask
    // succeeds. Waiting for the later tool_result leaves the card stuck in
    // "Submitting…" while the agent is already unblocked.
    resolveAskUserItem(s, a: { payload: { id: string; answers: AskUserAnswer[] } }) {
      for (let i = s.timeline.length - 1; i >= 0; i--) {
        const item = s.timeline[i]
        if (item.kind !== 'tool' || item.data.askUserId !== a.payload.id) continue
        item.data.status = 'done'
        item.data.output = formatAskUserOutput(a.payload.answers)
        item.data.askUserId = undefined
        item.data.askUserQuestions = undefined
        delete (item.data as ToolCall & { askUserTaskId?: string }).askUserTaskId
        return
      }
    },
  },
})

/** Mirror the ask_user tool's formatBatchResponse so optimistic receipts match
 *  the eventual tool_result / session replay text. */
export function formatAskUserOutput(answers: AskUserAnswer[]): string {
  if (answers.length === 0) return 'The user did not provide any answers.'
  if (answers.length === 1) {
    const ans = answers[0]
    const text = ans.answer || (ans.selected?.length ? ans.selected.join(', ') : '')
    if (!text) return 'The user did not provide an answer.'
    return `User's answer: ${text}`
  }
  return JSON.stringify({ answers })
}

// ═══════════════════════════════════════════════════════════════════════════
// session slice — sessions/tasks list, current session, project
// ═══════════════════════════════════════════════════════════════════════════

interface SessionState {
  sessions: SessionItem[]
  tasks: TaskItem[]
  /** Per-project last-activity timestamp (RFC3339), keyed by project path.
   *  Persisted server-side and bumped on session create / turn start / turn
   *  end — never on delete — so the sidebar's project ordering is stable
   *  across conversation deletions. */
  projectTimes: Record<string, string>
  /** Workspace classification for projectTimes entries, keyed by real path. */
  projectKinds: Record<string, WorkspaceKind>
  currentSessionId: string
  projectPath: string
  workspaceKind: WorkspaceKind
  wsConnected: boolean
}

const initialSession: SessionState = {
  sessions: [],
  tasks: [],
  projectTimes: {},
  projectKinds: {},
  currentSessionId: '',
  projectPath: '',
  workspaceKind: 'project',
  wsConnected: false,
}

const sessionSlice = createSlice({
  name: 'session',
  initialState: initialSession,
  reducers: {
    setSessions(s, a: { payload: SessionItem[] }) {
      // Same lazy-index race as setTasks: keep the open session if the server
      // hasn't written it to the index yet.
      const next = a.payload
      const seen = new Set(next.map((x) => x.uuid))
      const localOnly = s.sessions.filter(
        (x) => !seen.has(x.uuid) && x.uuid === s.currentSessionId,
      )
      s.sessions = localOnly.length ? [...localOnly, ...next] : next
    },
    setTasks(s, a: { payload: TaskItem[] }) {
      // Preserve a just-created local task that isn't in the server index yet
      // (session files are created lazily on the first user message).
      const next = a.payload
      const seen = new Set(next.map((t) => t.uuid))
      const localOnly = s.tasks.filter(
        (t) => !seen.has(t.uuid) && t.uuid === s.currentSessionId,
      )
      s.tasks = localOnly.length ? [...localOnly, ...next] : next
    },
    setCurrentSession(s, a: { payload: string }) {
      s.currentSessionId = a.payload
    },
    setProjectPath(s, a: { payload: string }) {
      s.projectPath = a.payload
    },
    setWorkspaceKind(s, a: { payload: WorkspaceKind | undefined }) {
      s.workspaceKind = a.payload === 'scratch' ? 'scratch' : 'project'
      if (s.projectPath) s.projectKinds[s.projectPath] = s.workspaceKind
    },
    setWsConnected(s, a: { payload: boolean }) {
      s.wsConnected = a.payload
    },
    setTaskRunning(s, a: { payload: { taskId: string; running: boolean } }) {
      const t = s.tasks.find((x) => x.uuid === a.payload.taskId)
      if (t) t.running = a.payload.running
    },
    /** Merge the server's per-project timestamps (GET /api/projects). Merges
     *  with a monotonic max per key instead of replacing: a live touch that
     *  lands while the fetch is in flight must not be clobbered by the stale
     *  snapshot. */
    setProjectTimes(s, a: { payload: ProjectInfo[] }) {
      for (const p of a.payload) {
        if (!p.path || !p.updated_at) continue
        if (isNewerTs(p.updated_at, s.projectTimes[p.path])) s.projectTimes[p.path] = p.updated_at
        s.projectKinds[p.path] = p.workspace_kind === 'scratch' ? 'scratch' : 'project'
      }
    },
    /** Live-bump one project's timestamp (a turn started/ended there). Mirrors
     *  the server-side touch: monotonic max, never moves backwards. */
    touchProjectTime(s, a: { payload: { path: string; ts: string } }) {
      const { path, ts } = a.payload
      if (!path || !ts) return
      if (isNewerTs(ts, s.projectTimes[path])) s.projectTimes[path] = ts
      if (!s.projectKinds[path]) {
        const taskKind = s.tasks.find((task) => task.project === path)?.workspace_kind
        s.projectKinds[path] = taskKind === 'scratch'
          ? 'scratch'
          : path === s.projectPath ? s.workspaceKind : 'project'
      }
    },
    /** Insert or merge a task so the sidebar shows it immediately. */
    upsertTask(s, a: { payload: TaskItem }) {
      const i = s.tasks.findIndex((t) => t.uuid === a.payload.uuid)
      if (i >= 0) {
        s.tasks[i] = { ...s.tasks[i], ...a.payload }
      } else {
        s.tasks.unshift(a.payload)
      }
    },
    /** Patch fields on an existing task (no-op if missing). */
    patchTask(s, a: { payload: { uuid: string } & Partial<TaskItem> }) {
      const t = s.tasks.find((x) => x.uuid === a.payload.uuid)
      if (!t) return
      const { uuid: _uuid, ...rest } = a.payload
      Object.assign(t, rest)
    },
    /** Remove a deleted session from BOTH lists, inside the reducer (operates
     *  on the latest state — a component-side filter over render-scope copies
     *  would clobber any list update that landed during the delete round-trip,
     *  and unlike setTasks/setSessions this never re-adds anything). */
    removeSession(s, a: { payload: string }) {
      s.sessions = s.sessions.filter((x) => x.uuid !== a.payload)
      s.tasks = s.tasks.filter((x) => x.uuid !== a.payload)
    },
    /** Insert or merge a session for the active-project fallback list. */
    upsertSession(s, a: { payload: SessionItem }) {
      const i = s.sessions.findIndex((x) => x.uuid === a.payload.uuid)
      if (i >= 0) {
        s.sessions[i] = { ...s.sessions[i], ...a.payload }
      } else {
        s.sessions.unshift(a.payload)
      }
    },
  },
})

// ═══════════════════════════════════════════════════════════════════════════
// conversation-load slice — foreground navigation + recoverable remote setup
// ═══════════════════════════════════════════════════════════════════════════

export interface ConversationLoadTarget {
  uuid: string
  project: string
  title?: string
  workspaceKind?: WorkspaceKind
}

export type ConversationLoadPhase =
  | 'idle'
  | 'loading'
  | 'connecting'
  | 'awaiting_host_key'
  | 'awaiting_auth'
  | 'activating'
  | 'error'

export type ConversationLoadIssue =
  | 'none'
  | 'authentication'
  | 'remote'
  | 'history'
  | 'host_key_unknown'
  | 'host_key_changed'
  | 'host_key_confirmation_mismatch'

export interface ConversationLoadState {
  requestId: string
  target: ConversationLoadTarget | null
  phase: ConversationLoadPhase
  historyStatus: 'idle' | 'loading' | 'ready' | 'error'
  environmentStatus: 'idle' | 'loading' | 'ready' | 'action_required' | 'error'
  previewTimeline: ThreadItem[]
  issue: ConversationLoadIssue
  error: string
  errorCode: string
  retryable: boolean
  hostKey: RemoteHostKeyErrorPayload | null
}

const initialConversationLoad: ConversationLoadState = {
  requestId: '',
  target: null,
  phase: 'idle',
  historyStatus: 'idle',
  environmentStatus: 'idle',
  previewTimeline: [],
  issue: 'none',
  error: '',
  errorCode: '',
  retryable: true,
  hostKey: null,
}

const conversationLoadSlice = createSlice({
  name: 'conversationLoad',
  initialState: initialConversationLoad,
  reducers: {
    begin(s, a: { payload: { requestId: string; target: ConversationLoadTarget } }) {
      s.requestId = a.payload.requestId
      s.target = a.payload.target
      s.phase = 'loading'
      s.historyStatus = 'loading'
      s.environmentStatus = 'loading'
      s.previewTimeline = []
      s.issue = 'none'
      s.error = ''
      s.errorCode = ''
      s.retryable = true
      s.hostKey = null
    },
    historyReady(s, a: { payload: { requestId: string; timeline: ThreadItem[] } }) {
      if (s.requestId !== a.payload.requestId) return
      s.historyStatus = 'ready'
      s.previewTimeline = a.payload.timeline
    },
    historyLoading(s, a: { payload: { requestId: string } }) {
      if (s.requestId !== a.payload.requestId) return
      s.historyStatus = 'loading'
      if (s.issue === 'history') {
        s.issue = 'none'
        s.error = ''
        s.errorCode = ''
        s.retryable = true
      }
    },
    historyFailed(s, a: { payload: { requestId: string; error: string } }) {
      if (s.requestId !== a.payload.requestId) return
      s.historyStatus = 'error'
      // History may still arrive in the activation response, so this does not
      // make the whole navigation terminal by itself.
      if (!s.error) s.error = a.payload.error
    },
    setPhase(s, a: { payload: { requestId: string; phase: ConversationLoadPhase; environmentStatus?: ConversationLoadState['environmentStatus'] } }) {
      if (s.requestId !== a.payload.requestId) return
      s.phase = a.payload.phase
      if (a.payload.environmentStatus) s.environmentStatus = a.payload.environmentStatus
      s.issue = 'none'
      s.error = ''
      s.errorCode = ''
      s.retryable = true
      s.hostKey = null
    },
    requireHostKey(s, a: { payload: { requestId: string; prompt: RemoteHostKeyErrorPayload } }) {
      if (s.requestId !== a.payload.requestId) return
      s.phase = 'awaiting_host_key'
      s.environmentStatus = 'action_required'
      s.hostKey = a.payload.prompt
      s.issue = hostKeyIssue(a.payload.prompt.code)
      s.error = a.payload.prompt.error
      s.errorCode = a.payload.prompt.code
      s.retryable = a.payload.prompt.code !== 'ssh_host_key_changed'
    },
    requireAuth(s, a: { payload: { requestId: string; error: string; code?: string; retryable?: boolean } }) {
      if (s.requestId !== a.payload.requestId) return
      s.phase = 'awaiting_auth'
      s.environmentStatus = 'action_required'
      s.issue = 'authentication'
      s.error = a.payload.error
      s.errorCode = a.payload.code || 'ssh_auth_required'
      s.retryable = a.payload.retryable !== false
      s.hostKey = null
    },
    failed(s, a: { payload: { requestId: string; error: string; issue?: ConversationLoadIssue; code?: string; retryable?: boolean } }) {
      if (s.requestId !== a.payload.requestId) return
      s.phase = 'error'
      s.issue = a.payload.issue || 'remote'
      if (s.issue !== 'history' || s.environmentStatus !== 'ready') {
        s.environmentStatus = 'error'
      }
      s.error = a.payload.error
      s.errorCode = a.payload.code || ''
      s.retryable = a.payload.retryable !== false
      s.hostKey = null
    },
    finish(s, a: { payload: { requestId: string } }) {
      if (s.requestId !== a.payload.requestId) return
      Object.assign(s, initialConversationLoad)
    },
    reset(s) {
      Object.assign(s, initialConversationLoad)
    },
  },
})

function hostKeyIssue(code: RemoteHostKeyErrorCode): ConversationLoadIssue {
  if (code === 'ssh_host_key_changed') return 'host_key_changed'
  if (code === 'ssh_host_key_confirmation_mismatch') return 'host_key_confirmation_mismatch'
  return 'host_key_unknown'
}

// ═══════════════════════════════════════════════════════════════════════════
// remote connection slice — quiet, task-scoped SSH/Docker recovery notices
// ═══════════════════════════════════════════════════════════════════════════

export interface RemoteConnectionNotice extends RemoteConnectionStatusData {
  task_id: string
  /** Changes for every wire update so a ready-dismiss timer cannot clear a
   * newer reconnect cycle for the same task. */
  revision: number
}

interface RemoteConnectionState {
  byTaskId: Record<string, RemoteConnectionNotice>
}

const initialRemoteConnection: RemoteConnectionState = { byTaskId: {} }

const remoteConnectionSlice = createSlice({
  name: 'remoteConnection',
  initialState: initialRemoteConnection,
  reducers: {
    statusReceived(s, a: { payload: RemoteConnectionStatusData }) {
      const taskId = a.payload.task_id
      if (!taskId) return
      const previous = s.byTaskId[taskId]
      const retryInMs = a.payload.retry_in_ms ?? a.payload.retry_after_ms
      s.byTaskId[taskId] = {
        ...a.payload,
        task_id: taskId,
        attempt: Math.max(0, a.payload.attempt || 0),
        max_attempts: Math.max(0, a.payload.max_attempts || 0),
        retry_in_ms: retryInMs === undefined ? undefined : Math.max(0, retryInMs),
        revision: (previous?.revision || 0) + 1,
      }
    },
    clearTransient(s, a: { payload: string }) {
      const current = s.byTaskId[a.payload]
      if (current?.status === 'waiting' || current?.status === 'reconnecting') {
        delete s.byTaskId[a.payload]
      }
    },
    clear(s, a: { payload: { taskId: string; revision?: number } }) {
      const current = s.byTaskId[a.payload.taskId]
      if (!current) return
      if (a.payload.revision !== undefined && current.revision !== a.payload.revision) return
      delete s.byTaskId[a.payload.taskId]
    },
    reset(s) {
      s.byTaskId = {}
    },
  },
})

// ═══════════════════════════════════════════════════════════════════════════
// model retry slice — task-scoped rate-limit backoff notices
// ═══════════════════════════════════════════════════════════════════════════

export interface ModelRetryNotice extends Omit<ModelRetryStatusData, 'status'> {
  task_id: string
  status: 'waiting' | 'ready'
  /** Guards the recovery timer against a newer retry cycle for this task. */
  revision: number
}

interface ModelRetryState {
  byTaskId: Record<string, ModelRetryNotice>
}

const initialModelRetry: ModelRetryState = { byTaskId: {} }

const modelRetrySlice = createSlice({
  name: 'modelRetry',
  initialState: initialModelRetry,
  reducers: {
    statusReceived(s, a: { payload: ModelRetryStatusData }) {
      const taskId = a.payload.task_id
      if (!taskId) return
      const previous = s.byTaskId[taskId]
      s.byTaskId[taskId] = {
        ...a.payload,
        task_id: taskId,
        status: a.payload.status === 'ready' ? 'ready' : 'waiting',
        attempt: Math.max(0, a.payload.attempt || 0),
        max_attempts: Math.max(0, a.payload.max_attempts || 0),
        retry_in_ms: a.payload.retry_in_ms === undefined
          ? undefined
          : Math.max(0, a.payload.retry_in_ms),
        revision: (previous?.revision || 0) + 1,
      }
    },
    clearWaiting(s, a: { payload: string }) {
      if (s.byTaskId[a.payload]?.status === 'waiting') delete s.byTaskId[a.payload]
    },
    clear(s, a: { payload: { taskId: string; revision?: number } }) {
      const current = s.byTaskId[a.payload.taskId]
      if (!current) return
      if (a.payload.revision !== undefined && current.revision !== a.payload.revision) return
      delete s.byTaskId[a.payload.taskId]
    },
    reset(s) {
      s.byTaskId = {}
    },
  },
})

// ═══════════════════════════════════════════════════════════════════════════
// model slice — provider/model/mode/favorites
// ═══════════════════════════════════════════════════════════════════════════

interface ModelState {
  providerName: string
  modelName: string
  // config.small_model as "provider/model"; '' = unset (follow the main model).
  smallModel: string
  // Global image-generation role as "provider/model"; independent from chat.
  imageModel: string
  mode: AgentMode
  providers: ProviderInfo[]
  favoriteModels: string[]
  recentModels: ModelRef[]
  effortOverrides: Record<string, string>
  agents: CustomAgentInfo[]
  agentName: string
  autoApprove: boolean
  imageSupport: boolean
  serverVersion: string
  maxIterations: number
}

const initialModel: ModelState = {
  providerName: '',
  modelName: '',
  smallModel: '',
  imageModel: '',
  mode: 'approval',
  providers: [],
  favoriteModels: [],
  recentModels: [],
  effortOverrides: {},
  agents: [],
  agentName: '',
  autoApprove: false,
  imageSupport: false,
  serverVersion: '',
  maxIterations: 0,
}

// Recompute imageSupport from the providers list for the currently selected
// model. Runs on every provider/model/providers change so the flag can never
// go stale when the user switches models (picker selectModel, WS
// model_changed). When the current model isn't in the list yet (providers not
// loaded), the last value — seeded from /health at startup — is kept.
function syncImageSupport(s: ModelState) {
  const cur = s.providers
    .find((p) => p.id === s.providerName)
    ?.models.find((m) => m.id === s.modelName)
  if (cur) {
    s.imageSupport = cur.input_modalities?.includes('image') ?? !!cur.image_support
  } else if (s.providers.length > 0) {
    // Providers are loaded but the current model isn't among them (e.g. set
    // via automation to an unlisted model) — don't carry the previous model's
    // capability forward.
    s.imageSupport = false
  }
}

const modelSlice = createSlice({
  name: 'model',
  initialState: initialModel,
  reducers: {
    setProvider(s, a: { payload: string }) {
      s.providerName = a.payload
      syncImageSupport(s)
    },
    setModel(s, a: { payload: string }) {
      s.modelName = a.payload
      syncImageSupport(s)
    },
    setSmallModel(s, a: { payload: string }) {
      s.smallModel = a.payload
    },
    setImageModel(s, a: { payload: string }) {
      s.imageModel = a.payload
    },
    setMode(s, a: { payload: AgentMode }) {
      s.mode = a.payload
    },
    setAgents(s, a: { payload: CustomAgentInfo[] }) {
      s.agents = a.payload
    },
    setAgent(s, a: { payload: string }) {
      s.agentName = a.payload
    },
    setProviders(s, a: { payload: ProviderInfo[] }) {
      s.providers = a.payload
      syncImageSupport(s)
    },
    setModelState(s, a: { payload: { recent: ModelRef[]; favorite: ModelRef[]; effortOverrides?: Record<string, string> } }) {
      s.recentModels = a.payload.recent
      s.favoriteModels = a.payload.favorite.map((r) => `${r.provider}/${r.model}`)
      s.effortOverrides = a.payload.effortOverrides ?? {}
    },
    setFavorite(s, a: { payload: { provider: string; model: string; favorite: boolean } }) {
      const key = `${a.payload.provider}/${a.payload.model}`
      if (a.payload.favorite) {
        if (!s.favoriteModels.includes(key)) s.favoriteModels.push(key)
      } else {
        s.favoriteModels = s.favoriteModels.filter((x) => x !== key)
      }
    },
    setEffortOverride(s, a: { payload: { provider: string; model: string; effort: string } }) {
      const key = `${a.payload.provider}/${a.payload.model}`
      if (a.payload.effort) s.effortOverrides[key] = a.payload.effort
      else delete s.effortOverrides[key]
    },
    setAutoApprove(s, a: { payload: boolean }) {
      s.autoApprove = a.payload
    },
    setImageSupport(s, a: { payload: boolean }) {
      s.imageSupport = a.payload
    },
    setServerVersion(s, a: { payload: string }) {
      s.serverVersion = a.payload
    },
    setMaxIterations(s, a: { payload: number }) {
      s.maxIterations = a.payload
    },
  },
})

// ═══════════════════════════════════════════════════════════════════════════
// App UI slice — view routing, dialog open states
// ═══════════════════════════════════════════════════════════════════════════

// M18: settings is a first-class view (its own full-page surface), not an
// overlay dialog — the bottom-left gear / ⌘, / CloudBadge all route here.
type View = 'chat' | 'automations' | 'cloud-mobile' | 'automation-run' | 'settings'

// Sections of the settings view. Deep links (e.g. CloudBadge → Cloud) set this
// together with setView('settings').
export type SettingsTab =
  | 'general'
  | 'cloud'
  | 'appearance'
  | 'agent'
  | 'review'
  | 'providers'
  | 'mcp'
  | 'skills'
  | 'memory'
  | 'browser'
  | 'computer'
  | 'ssh'
  | 'shortcuts'
  | 'usage'
  | 'developer'

interface UiState {
  activeView: View
  settingsTab: SettingsTab
  paletteOpen: boolean
  needsAuth: boolean
  needsSetup: boolean
  connectionError: string
  theme: string
  channelAvailable: boolean
  channelEnabled: boolean
  bleAvailable: boolean
  bleEnabled: boolean
}

const initialUi: UiState = {
  activeView: 'chat',
  settingsTab: 'general',
  paletteOpen: false,
  needsAuth: false,
  needsSetup: false,
  connectionError: '',
  theme: 'system',
  channelAvailable: false,
  channelEnabled: false,
  bleAvailable: false,
  bleEnabled: false,
}

const uiSlice = createSlice({
  name: 'ui',
  initialState: initialUi,
  reducers: {
    setView(s, a: { payload: View }) {
      s.activeView = a.payload
    },
    setSettingsTab(s, a: { payload: SettingsTab }) {
      s.settingsTab = a.payload
    },
    setPaletteOpen(s, a: { payload: boolean }) {
      s.paletteOpen = a.payload
    },
    setNeedsAuth(s, a: { payload: boolean }) {
      s.needsAuth = a.payload
    },
    setNeedsSetup(s, a: { payload: boolean }) {
      s.needsSetup = a.payload
    },
    setConnectionError(s, a: { payload: string }) {
      s.connectionError = a.payload
    },
    setTheme(s, a: { payload: string }) {
      s.theme = a.payload
    },
    setChannelState(s, a: { payload: { available: boolean; enabled: boolean } }) {
      s.channelAvailable = a.payload.available
      s.channelEnabled = a.payload.enabled
    },
    setBLEState(s, a: { payload: { available: boolean; enabled: boolean } }) {
      s.bleAvailable = a.payload.available
      s.bleEnabled = a.payload.enabled
    },
  },
})

// ═══════════════════════════════════════════════════════════════════════════
// Store wiring
// ═══════════════════════════════════════════════════════════════════════════

export const chatActions = chatSlice.actions
export const sessionActions = sessionSlice.actions
export const conversationLoadActions = conversationLoadSlice.actions
export const remoteConnectionActions = remoteConnectionSlice.actions
export const modelRetryActions = modelRetrySlice.actions
export const modelActions = modelSlice.actions
export const uiActions = uiSlice.actions

// Async thunks — wrap API calls + dispatch the right reducers.
export const sendMessage = createAsyncThunk(
  'chat/send',
  async (payload: { text: string; images?: import('jcode-ui-core').ChatImage[]; mode?: AgentMode; sessionId?: string; background?: boolean }, { dispatch, getState }) => {
    const state = getState() as RootState
    // sessionId override targets a specific (possibly background) session —
    // used when draining a stashed queue after that session's agentDone.
    const sessionId = payload.sessionId ?? (state.session.currentSessionId || undefined)
    const isCurrentForegroundSession = () =>
      (getState() as RootState).session.currentSessionId === (sessionId || '')
    const trimmed = payload.text.trim()
    // Goal flows are foreground-only: the goal API always targets the active
    // engine, so a background queue drain sends its text as a plain message.
    if (!payload.background && state.chat.goalArmed && trimmed && !trimmed.startsWith('/')) {
      dispatch(chatActions.setGoalArmed(false))
      dispatch(chatActions.addMessage({ role: 'user', content: payload.text }))
      const goal = await api.setGoal(trimmed, true, sessionId)
      if (!isCurrentForegroundSession()) return
      dispatch(chatActions.setGoal(goal))
      dispatch(chatActions.setRunning(true))
      return
    }
    // /goal slash interception (matches Vue store.sendMessage).
    if (!payload.background && (trimmed === '/goal' || trimmed.startsWith('/goal '))) {
      const objective = trimmed.slice('/goal'.length).trim()
      if (objective === '' || objective === 'status') {
        const goal = await api.goal(sessionId)
        if (!isCurrentForegroundSession()) return
        dispatch(chatActions.setGoal(goal))
        dispatch(chatActions.addMessage({
          role: 'system',
          content: goal ? `Goal is ${goal.status}: ${goal.objective}` : 'No active goal.',
        }))
        return
      }
      if (objective === 'clear') {
        await api.clearGoal(sessionId)
        if (!isCurrentForegroundSession()) return
        dispatch(chatActions.setGoal(null))
        dispatch(chatActions.addMessage({ role: 'system', content: 'Goal cleared.' }))
        return
      }
      dispatch(chatActions.addMessage({ role: 'user', content: payload.text }))
      const goal = await api.setGoal(objective, true, sessionId)
      if (!isCurrentForegroundSession()) return
      dispatch(chatActions.setGoal(goal))
      dispatch(chatActions.setRunning(true))
      return
    }
    // Foreground sends echo into the visible timeline; a background drain must
    // not touch the conversation the user is currently viewing.
    if (!payload.background) {
      dispatch(chatActions.addMessage({ role: 'user', content: payload.text, images: payload.images }))
      dispatch(chatActions.setRunning(true))
    }
    // First user turn materializes the session on disk — surface it in the
    // sidebar immediately (title + running) before the chat HTTP round-trip.
    if (sessionId) {
      revealSessionInSidebar(dispatch as AppDispatch, () => getState() as RootState, {
        uuid: sessionId,
        title: sessionTitleFromMessage(payload.text),
        running: true,
      })
    }
    const resp = await api.chat(payload.text, payload.mode, sessionId, payload.images)
    const sid = resp.session_id || sessionId
    if (sid) {
      if (!state.session.currentSessionId) dispatch(sessionActions.setCurrentSession(sid))
      revealSessionInSidebar(dispatch as AppDispatch, () => getState() as RootState, {
        uuid: sid,
        title: sessionTitleFromMessage(payload.text),
        running: true,
      })
      // Reconcile with the server index (now written by RecordUser).
      void dispatch(loadSessions())
      void dispatch(loadTasks())
    }
  },
)

/** Match backend generateTitle so the sidebar title doesn't flicker after refresh. */
function sessionTitleFromMessage(content: string): string {
  const first = content.split(/\r?\n/, 1)[0]?.trim() ?? ''
  if (!first) return 'New session'
  const chars = Array.from(first)
  return chars.length > 80 ? chars.slice(0, 80).join('') + '…' : first
}

/**
 * Ensure a session/task appears in the left sidebar immediately.
 * Backend only indexes a session after the first recorded message; empty
 * "new chat" UUIDs are otherwise invisible until the next full reload.
 *
 * Takes dispatch/getState directly (not an RTK thunk) so it can be called
 * from createAsyncThunk without AppDispatch vs ThunkDispatch type friction.
 */
export function revealSessionInSidebar(
  dispatch: AppDispatch,
  getState: () => RootState,
  opts: {
    uuid: string
    title?: string
    running?: boolean
    project?: string
    provider?: string
    model?: string
    workspaceKind?: WorkspaceKind
  },
) {
  if (!opts.uuid) return
  const state = getState()
  const now = new Date().toISOString()
  const project = opts.project || state.session.projectPath || ''
  const existing = state.session.tasks.find((t) => t.uuid === opts.uuid)
  // First non-empty title wins (matches backend generateTitle on first user msg).
  const title = existing?.title || opts.title || ''
  dispatch(sessionActions.upsertTask({
    uuid: opts.uuid,
    project: existing?.project || project,
    workspace_kind: opts.workspaceKind || existing?.workspace_kind || state.session.workspaceKind,
    created_at: existing?.created_at || now,
    updated_at: now,
    provider: opts.provider || existing?.provider || state.model.providerName || '',
    model: opts.model || existing?.model || state.model.modelName || '',
    title,
    pinned: existing?.pinned ?? false,
    archived: existing?.archived ?? false,
    unread: existing?.unread ?? false,
    status: existing?.status,
    running: opts.running ?? existing?.running ?? false,
  }))
  dispatch(sessionActions.upsertSession({
    uuid: opts.uuid,
    created_at: existing?.created_at || now,
    provider: opts.provider || existing?.provider || state.model.providerName || '',
    model: opts.model || existing?.model || state.model.modelName || '',
    title: title || undefined,
  }))
}

export const stopAgent = createAsyncThunk('chat/stop', async (_, { getState }) => {
  const state = getState() as RootState
  await api.stop(state.session.currentSessionId || undefined)
})

export const resolveApproval = createAsyncThunk(
  'approval/resolve',
  async (payload: { id: string; approved: boolean; approveAll?: boolean }, { dispatch, getState }) => {
    const state = getState() as RootState
    const foregroundTaskId = state.session.currentSessionId
    const item = state.chat.timeline.find((i) => i.kind === 'approval' && i.data.id === payload.id)
    const itemTaskId = item?.kind === 'approval' ? (item.data as Approval & { task_id?: string }).task_id : undefined
    const taskId = itemTaskId || foregroundTaskId || undefined
    dispatch(chatActions.setApprovalResolving({ id: payload.id, resolving: true }))
    try {
      await api.approval(payload.id, payload.approved, payload.approveAll ?? false, taskId)
      if ((getState() as RootState).session.currentSessionId !== foregroundTaskId) return
      dispatch(chatActions.resolveApprovalItem({ id: payload.id, approved: payload.approved }))
    } catch {
      if ((getState() as RootState).session.currentSessionId !== foregroundTaskId) return
      dispatch(chatActions.setApprovalResolving({ id: payload.id, resolving: false }))
    }
  },
)

export const resolveApprovalOption = createAsyncThunk(
  'approval/resolveOption',
  async (payload: { id: string; optionId: string }, { dispatch, getState }) => {
    const state = getState() as RootState
    const foregroundTaskId = state.session.currentSessionId
    const item = state.chat.timeline.find((entry) => entry.kind === 'approval' && entry.data.id === payload.id)
    const approval = item?.kind === 'approval' ? item.data as Approval & { task_id?: string } : undefined
    const option = approval?.options?.find((candidate) => candidate.id === payload.optionId)
    dispatch(chatActions.setApprovalResolving({ id: payload.id, resolving: true }))
    if (!approval || !option) {
      dispatch(chatActions.setApprovalResolving({ id: payload.id, resolving: false }))
      return
    }
    const taskId = approval.task_id || foregroundTaskId || undefined
    try {
      await api.approvalOption(payload.id, payload.optionId, taskId)
      if ((getState() as RootState).session.currentSessionId !== foregroundTaskId) return
      dispatch(chatActions.resolveApprovalItem({
        id: payload.id,
        approved: option.kind !== 'deny',
        optionId: payload.optionId,
      }))
    } catch {
      if ((getState() as RootState).session.currentSessionId !== foregroundTaskId) return
      dispatch(chatActions.setApprovalResolving({ id: payload.id, resolving: false }))
    }
  },
)

export const submitAskUser = createAsyncThunk(
  'askUser/submit',
  async (payload: { id: string; answers: AskUserAnswer[] }, { dispatch, getState }) => {
    const state = getState() as RootState
    const foregroundTaskId = state.session.currentSessionId
    let taskId: string | undefined
    for (const item of state.chat.timeline) {
      if (item.kind === 'tool' && item.data.askUserId === payload.id) {
        taskId = (item.data as ToolCall & { askUserTaskId?: string }).askUserTaskId
        break
      }
    }
    taskId ||= foregroundTaskId || undefined
    try {
      await api.askUser(payload.id, payload.answers, taskId)
      if ((getState() as RootState).session.currentSessionId !== foregroundTaskId) return
      // Hide the docked card immediately; the real tool_result may arrive later.
      dispatch(chatActions.resolveAskUserItem({ id: payload.id, answers: payload.answers }))
    } catch (error) {
      if ((getState() as RootState).session.currentSessionId !== foregroundTaskId) throw error
      // surface in timeline as a system message
      dispatch(chatActions.addMessage({ role: 'system', content: 'Failed to submit answer', level: 'error' }))
      throw error
    }
  },
)

export const editMessage = createAsyncThunk(
  'chat/edit',
  async (payload: { id: string; text: string }, { dispatch, getState }) => {
    const state = getState() as RootState
    const msgIdx = state.chat.timeline.findIndex((i) => i.kind === 'message' && i.data.id === payload.id && i.data.role === 'user')
    if (msgIdx < 0) return
    const beforeUserMessage = state.chat.timeline
      .slice(0, msgIdx)
      .filter((i) => i.kind === 'message' && i.data.role === 'user')
      .length
    try {
      const res = await api.truncateHistory(beforeUserMessage)
      if (res.session_id) dispatch(sessionActions.setCurrentSession(res.session_id))
    } catch (e) {
      dispatch(chatActions.addMessage({
        role: 'system',
        content: e instanceof Error ? e.message : 'Failed to truncate history',
        level: 'error',
      }))
      return
    }
    dispatch(chatActions.truncateTimelineFrom(payload.id))
    await dispatch(sendMessage({ text: payload.text }))
  },
)

export const loadSessions = createAsyncThunk('session/load', async (_, { dispatch }) => {
  const sessions = await api.sessions()
  dispatch(sessionActions.setSessions(sessions))
})

export const loadTasks = createAsyncThunk('tasks/load', async (_, { dispatch }) => {
  const tasks = await api.tasks()
  dispatch(sessionActions.setTasks(tasks))
})

export const loadProjects = createAsyncThunk('projects/load', async (_, { dispatch }) => {
  const projects = await api.projects()
  dispatch(sessionActions.setProjectTimes(projects))
})

export const loadSlashCommands = createAsyncThunk('chat/loadSlash', async (_, { dispatch, getState }) => {
  const taskID = (getState() as RootState).session.currentSessionId
  const cmds = await api.slashCommands(taskID || undefined)
  if ((getState() as RootState).session.currentSessionId !== taskID) return
  dispatch(chatActions.setSlashCommands(cmds))
})

export const loadModels = createAsyncThunk('model/loadModels', async (_, { dispatch, getState }) => {
  const taskID = (getState() as RootState).session.currentSessionId
  const data = await api.models(taskID || undefined)
  if ((getState() as RootState).session.currentSessionId !== taskID) return
  dispatch(modelActions.setProviders(data.providers || []))
  dispatch(modelActions.setProvider(data.current.provider))
  dispatch(modelActions.setModel(data.current.model))
  dispatch(modelActions.setImageModel(
    data.current_image?.provider && data.current_image?.model
      ? `${data.current_image.provider}/${data.current_image.model}`
      : '',
  ))
  const provider = data.providers.find((p) => p.id === data.current.provider)
  const model = provider?.models.find((m) => m.id === data.current.model)
  dispatch(modelActions.setImageSupport(model?.input_modalities?.includes('image') ?? !!model?.image_support))
})

export const loadAgents = createAsyncThunk('model/loadAgents', async (_, { dispatch, getState }) => {
  const taskID = (getState() as RootState).session.currentSessionId
  const data = await api.agents(taskID || undefined)
  if ((getState() as RootState).session.currentSessionId !== taskID) return
  dispatch(modelActions.setAgents(data.agents || []))
  dispatch(modelActions.setAgent(data.current || ''))
})

export const loadModelState = createAsyncThunk('model/loadState', async (_, { dispatch }) => {
  const data = await api.modelState()
  dispatch(modelActions.setModelState({
    recent: data.recent || [],
    favorite: data.favorite || [],
    effortOverrides: data.effort_overrides || {},
  }))
})

export const loadApprovalMode = createAsyncThunk('model/loadApprovalMode', async (_, { dispatch, getState }) => {
  const taskID = (getState() as RootState).session.currentSessionId
  const data = await api.approvalMode(taskID || undefined)
  if ((getState() as RootState).session.currentSessionId !== taskID) return
  dispatch(modelActions.setAutoApprove(data.auto_approve))
  const state = getState() as RootState
  if (data.auto_approve) dispatch(modelActions.setMode('full_access'))
  else if (state.model.mode === 'full_access') dispatch(modelActions.setMode('approval'))
})

export const loadChannelState = createAsyncThunk('ui/loadChannelState', async (_, { dispatch }) => {
  const data = await api.channelStatus()
  dispatch(uiActions.setChannelState({ available: data.available, enabled: data.state === 'enabled' }))
})

export const loadBLEState = createAsyncThunk('ui/loadBLEState', async (_, { dispatch }) => {
  const data = await api.channelBLEStatus()
  dispatch(uiActions.setBLEState({ available: data.available, enabled: data.enabled }))
})

export const loadConfig = createAsyncThunk('model/loadConfig', async (_, { dispatch }) => {
  const cfg = await api.config()
  dispatch(modelActions.setMaxIterations(cfg.max_iterations))
  dispatch(modelActions.setSmallModel(cfg.small_model ?? ''))
  if (cfg.language && (SUPPORTED_LOCALES as readonly string[]).includes(cfg.language)) {
    await setLocale(cfg.language as (typeof SUPPORTED_LOCALES)[number])
  }
  if (cfg.theme) {
    hydrateTheme(cfg.theme)
    dispatch(uiActions.setTheme(cfg.theme))
  }
})

export const loadStatus = createAsyncThunk('app/loadStatus', async (_, { dispatch, getState }) => {
  const taskID = (getState() as RootState).session.currentSessionId
  const status = await api.status(taskID || undefined)
  if ((getState() as RootState).session.currentSessionId !== taskID) return
  dispatch(chatActions.setRunning(!!status.running))
  dispatch(sessionActions.setProjectPath(status.project || status.workspace_key || status.pwd))
  dispatch(sessionActions.setWorkspaceKind(status.workspace_kind))
  dispatch(modelActions.setProvider(status.provider))
  dispatch(modelActions.setModel(status.model))
  dispatch(modelActions.setAgent(status.agent || ''))
  dispatch(modelActions.setMode(normalizeMode(status.mode)))
  if (status.token) dispatch(chatActions.setTokenSnapshot(status.token))
})

export const loadGoal = createAsyncThunk('chat/loadGoal', async (_, { dispatch, getState }) => {
  const taskID = (getState() as RootState).session.currentSessionId
  const goal = await api.goal(taskID || undefined)
  if ((getState() as RootState).session.currentSessionId !== taskID) return
  dispatch(chatActions.setGoal(goal))
})

export const loadTodos = createAsyncThunk('chat/loadTodos', async (_, { dispatch, getState }) => {
  const taskID = (getState() as RootState).session.currentSessionId
  const todos = await api.todos(taskID || undefined)
  if ((getState() as RootState).session.currentSessionId !== taskID) return
  dispatch(chatActions.setTodos(todos))
})

export const reconcilePendingInteractions = createAsyncThunk('chat/reconcilePending', async (_, { dispatch, getState }) => {
  const taskID = (getState() as RootState).session.currentSessionId
  const [askResult, approvalResult] = await Promise.allSettled([
    api.askPending(taskID || undefined),
    api.approvalPending(taskID || undefined),
  ])
  if ((getState() as RootState).session.currentSessionId !== taskID) return
  if (askResult.status === 'fulfilled') {
    for (const req of askResult.value) {
      dispatch(chatActions.attachAskUser({
        toolName: 'ask_user',
        askUserId: req.id,
        questions: req.questions,
        taskId: req.task_id,
      }))
    }
  }
  if (approvalResult.status === 'fulfilled') {
    const state = getState() as RootState
    const existing = new Set(
      state.chat.timeline
        .filter((i) => i.kind === 'approval')
        .map((i) => i.kind === 'approval' ? i.data.id : ''),
    )
    for (const req of approvalResult.value) {
      if (existing.has(req.id)) continue
      const approval: Approval = {
        id: req.id,
        tool_name: req.tool_name,
        tool_args: req.tool_args,
        tool_call_id: req.tool_call_id,
        is_external: req.is_external,
        approvalClass: req.approval_class,
        options: req.options,
        billableSummary: req.billable_summary,
        resolvedOptionId: req.resolved_option_id,
      }
      ;(approval as Approval & { task_id?: string }).task_id = req.task_id
      dispatch(chatActions.addApprovalRequest(approval))
      existing.add(req.id)
    }
  }
})

export const loadWorkspaceState = createAsyncThunk('app/loadWorkspaceState', async (_, { dispatch }) => {
  await Promise.allSettled([
    dispatch(loadStatus()),
    dispatch(loadConfig()),
    dispatch(loadModels()),
    dispatch(loadAgents()),
    dispatch(loadModelState()),
    dispatch(loadSessions()),
    dispatch(loadTasks()),
    dispatch(loadProjects()),
    dispatch(loadSlashCommands()),
    dispatch(loadApprovalMode()),
    dispatch(loadChannelState()),
    dispatch(loadBLEState()),
    dispatch(loadGoal()),
    dispatch(loadTodos()),
    dispatch(reconcilePendingInteractions()),
  ])
})

export interface ConversationRemoteCredentials {
  authMethod: 'key' | 'password'
  password?: string
  keyPath?: string
  passphrase?: string
}

type ConversationHistoryResult = {
  entries?: SessionEntry[]
  error?: string
}

let activeConversationLoad: {
  requestId: string
  targetId: string
  controller: AbortController
  /** Ephemeral only: preserve an explicitly submitted credential across the
   * host-key confirmation round trip without putting secrets in Redux. */
  credentials?: ConversationRemoteCredentials
} | null = null
let conversationNavigationGeneration = 0
const conversationHistories = new Map<string, Promise<ConversationHistoryResult>>()
type PendingConversationEvent = () => void
const maxPendingConversationEvents = 256
let pendingConversationEvents: {
  requestId: string
  targetId: string
  events: PendingConversationEvent[]
} | null = null
export const conversationLoadTimeouts = {
  historyMs: 15_000,
  activationMs: 30_000,
} as const

function deadlineSignal(parent: AbortSignal, timeoutMs: number) {
  const controller = new AbortController()
  let timedOut = false
  let disposed = false
  let timer: ReturnType<typeof setTimeout> | undefined
  const dispose = () => {
    if (disposed) return
    disposed = true
    if (timer !== undefined) clearTimeout(timer)
    parent.removeEventListener('abort', onParentAbort)
  }
  const onParentAbort = () => {
    dispose()
    controller.abort(parent.reason)
  }
  if (parent.aborted) {
    controller.abort(parent.reason)
  } else {
    parent.addEventListener('abort', onParentAbort, { once: true })
    timer = setTimeout(() => {
      dispose()
      timedOut = true
      controller.abort(new DOMException('Request timed out', 'TimeoutError'))
    }, timeoutMs)
  }
  return {
    signal: controller.signal,
    timedOut: () => timedOut,
    dispose,
  }
}

function resetPendingConversationEvents(requestId: string, targetId: string) {
  pendingConversationEvents = { requestId, targetId, events: [] }
}

function clearPendingConversationEvents(requestId?: string) {
  if (!requestId || pendingConversationEvents?.requestId === requestId) pendingConversationEvents = null
}

/** Queue a task-scoped WS mutation while its durable transcript is visible in
 * the loading preview but has not yet become the committed foreground. */
export function bufferPendingConversationEvent(
  requestId: string,
  targetId: string,
  apply: PendingConversationEvent,
): boolean {
  if (
    activeConversationLoad?.requestId !== requestId ||
    activeConversationLoad.targetId !== targetId ||
    pendingConversationEvents?.requestId !== requestId ||
    pendingConversationEvents.targetId !== targetId
  ) return false
  pendingConversationEvents.events.push(apply)
  if (pendingConversationEvents.events.length > maxPendingConversationEvents) {
    pendingConversationEvents.events.shift()
  }
  return true
}

function flushPendingConversationEvents(requestId: string, targetId: string) {
  if (
    pendingConversationEvents?.requestId !== requestId ||
    pendingConversationEvents.targetId !== targetId
  ) return
  const events = pendingConversationEvents.events
  pendingConversationEvents = null
  for (const apply of events) apply()
}

function isConversationLoadCurrent(getState: () => unknown, requestId: string): boolean {
  return (getState() as RootState).conversationLoad.requestId === requestId
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException
    ? error.name === 'AbortError'
    : error instanceof Error && error.name === 'AbortError'
}

function remoteHostKeyPrompt(error: unknown): RemoteHostKeyErrorPayload | null {
  if (!isAPIError(error) || error.status !== 409 || !error.body || typeof error.body !== 'object') return null
  const body = error.body as Partial<RemoteHostKeyErrorPayload>
  if (
    body.code !== 'ssh_host_key_unknown' &&
    body.code !== 'ssh_host_key_changed' &&
    body.code !== 'ssh_host_key_confirmation_mismatch'
  ) return null
  if (!body.host || !body.fingerprint || !body.key_type) return null
  return {
    error: body.error || error.message,
    code: body.code,
    host: body.host,
    fingerprint: body.fingerprint,
    key_type: body.key_type,
    old_fingerprint: body.old_fingerprint,
    expected_fingerprint: body.expected_fingerprint,
  }
}

function looksLikeSSHAuthenticationError(error: unknown): boolean {
  if (isAPIError(error) && error.body && typeof error.body === 'object') {
    if ((error.body as { code?: unknown }).code === 'ssh_auth_required') return true
  }
  const message = error instanceof Error ? error.message : String(error)
  return /auth|permission denied|private key|public key|passphrase|password/i.test(message)
}

function conversationLoadError(error: unknown): { error: string; code?: string; retryable: boolean } {
  const message = error instanceof Error ? error.message : String(error)
  if (!isAPIError(error) || !error.body || typeof error.body !== 'object') {
    return { error: message, retryable: true }
  }
  const body = error.body as { code?: unknown; retryable?: unknown }
  return {
    error: message,
    code: typeof body.code === 'string' ? body.code : error.code,
    retryable: body.retryable !== false,
  }
}

function beginConversationHistory(
  requestId: string,
  target: ConversationLoadTarget,
  signal: AbortSignal,
  dispatch: AppDispatch,
  getState: () => unknown,
): Promise<ConversationHistoryResult> {
  dispatch(conversationLoadActions.historyLoading({ requestId }))
  const history = (async (): Promise<ConversationHistoryResult> => {
    const deadline = deadlineSignal(signal, conversationLoadTimeouts.historyMs)
    try {
      const entries = await api.session(target.uuid, deadline.signal)
      if (isConversationLoadCurrent(getState, requestId)) {
        // The GET response is the durable barrier. Only WS frames received
        // after this point need to be layered over the replay at commit.
        resetPendingConversationEvents(requestId, target.uuid)
        // A preview is deliberately never marked running: it is a read-only
        // durable transcript while the environment catches up.
        dispatch(conversationLoadActions.historyReady({
          requestId,
          timeline: replayTimeline(entries, false),
        }))
      }
      return { entries }
    } catch (error) {
      if (deadline.timedOut()) {
        const message = i18n.t('conversationLoading.error.historyTimeout')
        if (isConversationLoadCurrent(getState, requestId)) {
          dispatch(conversationLoadActions.historyFailed({ requestId, error: message }))
        }
        return { error: message }
      }
      if (isAbortError(error)) return {}
      const message = error instanceof Error ? error.message : String(error)
      if (isConversationLoadCurrent(getState, requestId)) {
        dispatch(conversationLoadActions.historyFailed({ requestId, error: message }))
      }
      return { error: message }
    } finally {
      deadline.dispose()
    }
  })()
  conversationHistories.set(requestId, history)
  return history
}

function sshConnectRequest(
  target: ConversationLoadTarget,
  credentials?: ConversationRemoteCredentials,
  confirmedFingerprint?: string,
): RemoteConnectRequest {
  const remote = parseRemoteLabel(target.project)
  if (!remote || remote.kind !== 'ssh') throw new Error('Invalid SSH workspace address')
  const authMethod = credentials?.authMethod
  return {
    type: 'ssh',
    host: remote.host,
    port: remote.port || 22,
    user: remote.user || 'root',
    // Omit authentication fields when the user only confirmed a host key.
    // The backend then preserves its ssh-agent/default-key fallback chain.
    auth_method: authMethod,
    password: authMethod === 'password' ? credentials?.password : undefined,
    key_path: authMethod === 'key' ? credentials?.keyPath : undefined,
    passphrase: authMethod === 'key' ? credentials?.passphrase : undefined,
    accept_host_key: confirmedFingerprint ? true : undefined,
    host_key_fingerprint: confirmedFingerprint,
  }
}

async function connectAndBindConversation(
  target: ConversationLoadTarget,
  credentials: ConversationRemoteCredentials | undefined,
  confirmedFingerprint: string | undefined,
  signal: AbortSignal,
): Promise<SessionActivationResponse> {
  const remote = parseRemoteLabel(target.project)
  if (!remote || remote.kind !== 'ssh') throw new Error('Invalid SSH workspace address')
  const request = sshConnectRequest(target, credentials, confirmedFingerprint)
  let connectionId = ''
  let bound = false
  try {
    const connection = await api.remoteConnect(request, signal)
    connectionId = connection.connection_id
    const bind = await api.remoteBind(
      connection.connection_id,
      remote.remotePath || connection.remote_pwd,
      { session_id: target.uuid, focus: false },
      signal,
    )
    bound = true
    if (!bind.session_id) throw new Error('Remote workspace did not activate the requested conversation')
    return {
      status: bind.status,
      session_id: bind.session_id,
      kind: bind.kind || 'ssh',
      pwd: bind.pwd,
      project: bind.project || bind.label || target.project,
      workspace_key: bind.workspace_key || bind.project || bind.label || target.project,
      workspace_kind: bind.workspace_kind || 'project',
      provider: bind.provider,
      model: bind.model,
      agent: bind.agent,
      mode: bind.mode || 'approval',
      running: !!bind.running,
      activated: !!bind.activated,
      focused: !!bind.focused,
    }
  } finally {
    if (connectionId && !bound) void api.remoteCancel(connectionId).catch(() => undefined)
  }
}

async function commitConversation(
  requestId: string,
  target: ConversationLoadTarget,
  response: SessionActivationResponse,
  history: ConversationHistoryResult,
  dispatch: AppDispatch,
  getState: () => unknown,
): Promise<void> {
  if (!isConversationLoadCurrent(getState, requestId)) return
  const timeline = history.entries ? replayTimeline(history.entries, !!response.running) : undefined
  if (!timeline) throw new Error(history.error || 'Conversation history is unavailable')

  dispatch(chatActions.clearChat())
  dispatch(chatActions.setTimeline(timeline))
  const resumedId = response.session_id || target.uuid
  dispatch(sessionActions.setCurrentSession(resumedId))
  dispatch(sessionActions.setProjectPath(response.project || response.workspace_key || target.project || response.pwd))
  dispatch(sessionActions.setWorkspaceKind(response.workspace_kind || target.workspaceKind))
  dispatch(remoteConnectionActions.clear({ taskId: resumedId }))
  dispatch(chatActions.setRunning(!!response.running))
  dispatch(modelActions.setProvider(response.provider || ''))
  dispatch(modelActions.setModel(response.model || ''))
  dispatch(modelActions.setAgent(response.agent || ''))
  dispatch(modelActions.setMode(normalizeMode(response.mode || '')))
  flushPendingConversationEvents(requestId, resumedId)
  // Activation deliberately has no transcript/task-detail payload. These
  // task-owned details hydrate independently after the atomic foreground commit.
  void dispatch(loadGoal())
  void dispatch(loadTodos())
  void dispatch(reconcilePendingInteractions())
  void dispatch(loadSessions())
  void dispatch(loadTasks())
  void dispatch(loadProjects())
  dispatch(conversationLoadActions.finish({ requestId }))
  conversationHistories.delete(requestId)
  clearPendingConversationEvents(requestId)
  if (activeConversationLoad?.requestId === requestId) activeConversationLoad = null
}

async function resumeConversationEnvironment(
  requestId: string,
  target: ConversationLoadTarget,
  credentials: ConversationRemoteCredentials | undefined,
  confirmedFingerprint: string | undefined,
  dispatch: AppDispatch,
  getState: () => unknown,
): Promise<void> {
  const active = activeConversationLoad
  if (!active || active.requestId !== requestId || !isConversationLoadCurrent(getState, requestId)) return
  if (credentials) active.credentials = credentials
  const effectiveCredentials = credentials || active.credentials
  const manualSSH = !!effectiveCredentials || !!confirmedFingerprint
  dispatch(conversationLoadActions.setPhase({
    requestId,
    phase: manualSSH ? 'connecting' : 'activating',
    environmentStatus: 'loading',
  }))
  try {
    const deadline = deadlineSignal(active.controller.signal, conversationLoadTimeouts.activationMs)
    let response: SessionActivationResponse
    try {
      response = manualSSH
        ? await connectAndBindConversation(target, effectiveCredentials, confirmedFingerprint, deadline.signal)
        : await api.activateSession({
            session_id: target.uuid,
            project_path: target.project || undefined,
            focus: false,
          }, deadline.signal)
    } catch (error) {
      if (deadline.timedOut()) throw new Error(i18n.t('conversationLoading.error.activationTimeout'))
      throw error
    } finally {
      deadline.dispose()
    }
    if (!isConversationLoadCurrent(getState, requestId)) return
    dispatch(conversationLoadActions.setPhase({
      requestId,
      phase: 'loading',
      environmentStatus: 'ready',
    }))
    const history = await (
      conversationHistories.get(requestId) || Promise.resolve<ConversationHistoryResult>({})
    )
    if (!isConversationLoadCurrent(getState, requestId)) return
    if (!history.entries) throw new Error(history.error || 'Conversation history is unavailable')

    // Preparing an engine is deliberately non-focusing. Once the transcript is
    // ready, focus the exact hydrated session and synchronously commit it to the
    // foreground; Cancel before this point leaves the previous engine active.
    dispatch(conversationLoadActions.setPhase({
      requestId,
      phase: 'activating',
      environmentStatus: 'ready',
    }))
    const focusDeadline = deadlineSignal(active.controller.signal, conversationLoadTimeouts.activationMs)
    try {
      response = await api.activateSession({
        session_id: response.session_id || target.uuid,
        project_path: response.project || response.workspace_key || target.project || undefined,
        focus: true,
      }, focusDeadline.signal)
    } catch (error) {
      if (focusDeadline.timedOut()) throw new Error(i18n.t('conversationLoading.error.activationTimeout'))
      throw error
    } finally {
      focusDeadline.dispose()
    }
    if (!isConversationLoadCurrent(getState, requestId)) return
    await commitConversation(requestId, target, response, history, dispatch, getState)
  } catch (error) {
    if (isAbortError(error) || !isConversationLoadCurrent(getState, requestId)) return
    const prompt = remoteHostKeyPrompt(error)
    if (prompt) {
      dispatch(conversationLoadActions.requireHostKey({ requestId, prompt }))
      return
    }
    if (target.project.startsWith('ssh://') && looksLikeSSHAuthenticationError(error)) {
      const issue = conversationLoadError(error)
      dispatch(conversationLoadActions.requireAuth({
        requestId,
        ...issue,
      }))
      return
    }
    const loadState = (getState() as RootState).conversationLoad
    const historyOnly = loadState.environmentStatus === 'ready' && loadState.historyStatus === 'error'
    const issue = conversationLoadError(error)
    dispatch(conversationLoadActions.failed({
      requestId,
      ...issue,
      issue: historyOnly ? 'history' : 'remote',
    }))
  }
}

/** Open an existing conversation. History and environment activation begin at
 *  the same time; only the newest request is allowed to commit. */
export const openConversation = createAsyncThunk(
  'conversation/open',
  async (input: ConversationLoadTarget, { dispatch, getState, requestId }) => {
    conversationNavigationGeneration += 1
    const previousRequestId = activeConversationLoad?.requestId
    activeConversationLoad?.controller.abort()
    if (previousRequestId) {
      conversationHistories.delete(previousRequestId)
      clearPendingConversationEvents(previousRequestId)
    }
    const controller = new AbortController()
    activeConversationLoad = { requestId, targetId: input.uuid, controller }
    const state = getState() as RootState
    const indexedTask = state.session.tasks.find((task) => task.uuid === input.uuid)
    const indexedSession = state.session.sessions.find((session) => session.uuid === input.uuid)
    const target: ConversationLoadTarget = {
      uuid: input.uuid,
      project: input.project || indexedTask?.project || state.session.projectPath,
      title: input.title || indexedTask?.title || indexedSession?.title,
      workspaceKind: input.workspaceKind || indexedTask?.workspace_kind || state.session.workspaceKind,
    }
    dispatch(uiActions.setView('chat'))
    dispatch(conversationLoadActions.begin({ requestId, target }))
    beginConversationHistory(requestId, target, controller.signal, dispatch as AppDispatch, getState)
    await resumeConversationEnvironment(
      requestId,
      target,
      undefined,
      undefined,
      dispatch as AppDispatch,
      getState,
    )
  },
)

/** Retry a task whose transparent remote recovery was exhausted. Ordinary
 * transport failures are retried in place; credential/host-key problems move
 * into the existing ConversationLoadingView's inline action flow (never the
 * new-workspace RemoteConnectWizard modal). */
export const retryRemoteConnection = createAsyncThunk(
  'remoteConnection/retry',
  async (input: { taskId: string }, { dispatch, getState }) => {
    const initial = getState() as RootState
    const notice = initial.remoteConnection.byTaskId[input.taskId]
    if (!notice) return
    if (initial.session.currentSessionId === input.taskId && initial.chat.isRunning) return
    const task = initial.session.tasks.find((candidate) => candidate.uuid === input.taskId)
    const session = initial.session.sessions.find((candidate) => candidate.uuid === input.taskId)
    const target: ConversationLoadTarget = {
      uuid: input.taskId,
      project: task?.project || (initial.session.currentSessionId === input.taskId ? initial.session.projectPath : ''),
      title: task?.title || session?.title,
      workspaceKind: task?.workspace_kind || initial.session.workspaceKind,
    }

    if (notice.status === 'action_required') {
      await dispatch(openConversation(target))
      return
    }

    dispatch(remoteConnectionActions.statusReceived({
      ...notice,
      status: 'reconnecting',
      attempt: 0,
      max_attempts: 0,
      retry_in_ms: undefined,
      retry_after_ms: undefined,
      error: undefined,
      code: undefined,
    }))

    try {
      // Repair without changing focus. Existing foreground runtimes retain
      // their active pointer during the atomic swap; cold/background tasks are
      // routed by task id. A second focus request would introduce a TOCTOU in
      // which a late response could steal focus after the user switched tasks.
      const response = await api.activateSession({
        session_id: input.taskId,
        project_path: target.project || undefined,
        source: isTauri ? 'desktop' : undefined,
        focus: false,
      })
      const current = getState() as RootState
      if (current.session.currentSessionId !== input.taskId) {
        dispatch(remoteConnectionActions.clear({ taskId: input.taskId }))
        return
      }
      dispatch(sessionActions.setProjectPath(response.project || response.workspace_key || target.project || response.pwd))
      dispatch(sessionActions.setWorkspaceKind(response.workspace_kind || target.workspaceKind))
      dispatch(remoteConnectionActions.statusReceived({
        task_id: input.taskId,
        kind: response.kind === 'docker' ? 'docker' : 'ssh',
        status: 'ready',
        attempt: 0,
        max_attempts: 0,
        host: notice.host,
      }))
    } catch (error) {
      if ((getState() as RootState).session.currentSessionId !== input.taskId) {
        dispatch(remoteConnectionActions.clear({ taskId: input.taskId }))
        return
      }
      const issue = remoteRetryIssue(error, notice.kind)
      if (issue.actionRequired) {
        // The dedicated load view owns host-key confirmation and credentials.
        // Reusing it also preserves the conversation's session-aware bind.
        dispatch(remoteConnectionActions.statusReceived({
          task_id: input.taskId,
          kind: notice.kind,
          status: 'action_required',
          attempt: notice.attempt,
          max_attempts: notice.max_attempts,
          host: notice.host,
          error: issue.error,
          code: issue.code,
          retryable: issue.retryable,
        }))
        await dispatch(openConversation(target))
        return
      }
      dispatch(remoteConnectionActions.statusReceived({
        task_id: input.taskId,
        kind: notice.kind,
        status: 'failed',
        attempt: notice.attempt,
        max_attempts: notice.max_attempts,
        host: notice.host,
        error: issue.error,
        code: issue.code,
        retryable: issue.retryable,
      }))
    }
  },
)

function remoteRetryIssue(error: unknown, kind: RemoteConnectionStatusData['kind']): {
  error: string
  code?: string
  retryable: boolean
  actionRequired: boolean
} {
  const body = isAPIError(error) && error.body && typeof error.body === 'object'
    ? error.body as Record<string, unknown>
    : undefined
  const code = isAPIError(error) ? error.code : undefined
  const actionRequired = code === 'ssh_auth_required' || code === 'ssh_host_key_unknown' ||
    code === 'ssh_host_key_changed' || code === 'ssh_host_key_confirmation_mismatch'
  return {
    error: error instanceof Error
      ? error.message
      : i18n.t('remoteConnection.failed.title', {
          transport: i18n.t(`remoteConnection.transport.${kind}`),
        }),
    code,
    retryable: typeof body?.retryable === 'boolean' ? body.retryable : !actionRequired,
    actionRequired,
  }
}

export const continueConversationLoad = createAsyncThunk(
  'conversation/continue',
  async (
    input: { requestId: string; credentials?: ConversationRemoteCredentials; acceptHostKey?: boolean },
    { dispatch, getState },
  ) => {
    const state = getState() as RootState
    if (state.conversationLoad.requestId !== input.requestId || !state.conversationLoad.target) return
    const target = state.conversationLoad.target
    const confirmedFingerprint = input.acceptHostKey ? state.conversationLoad.hostKey?.fingerprint : undefined
    if (!activeConversationLoad || activeConversationLoad.requestId !== input.requestId || activeConversationLoad.controller.signal.aborted) {
      const retainedCredentials = activeConversationLoad?.requestId === input.requestId
        ? activeConversationLoad.credentials
        : undefined
      activeConversationLoad = {
        requestId: input.requestId,
        targetId: target.uuid,
        controller: new AbortController(),
        credentials: retainedCredentials,
      }
    }
    if (state.conversationLoad.historyStatus === 'error') {
      beginConversationHistory(
        input.requestId,
        target,
        activeConversationLoad.controller.signal,
        dispatch as AppDispatch,
        getState,
      )
    }
    await resumeConversationEnvironment(
      input.requestId,
      target,
      input.credentials,
      confirmedFingerprint,
      dispatch as AppDispatch,
      getState,
    )
  },
)

export const cancelConversationLoad = createAsyncThunk(
  'conversation/cancel',
  async (_, { dispatch, getState }) => {
    conversationNavigationGeneration += 1
    const requestId = (getState() as RootState).conversationLoad.requestId
    if (activeConversationLoad?.requestId === requestId) {
      activeConversationLoad.controller.abort()
      activeConversationLoad = null
    }
    conversationHistories.delete(requestId)
    clearPendingConversationEvents(requestId)
    dispatch(conversationLoadActions.reset())
  },
)

/** Replay the already-focused session without mutating backend focus. This is
 * used to repair a missing WS tool lifecycle host, so it must be a pure history
 * GET and must not outlive a foreground navigation that started after it. */
export const loadSession = createAsyncThunk(
  'session/loadOne',
  async (input: string | { uuid: string; background?: boolean }, { dispatch, getState }) => {
    const uuid = typeof input === 'string' ? input : input.uuid
    const background = typeof input === 'object' && !!input.background
    const startState = getState() as RootState
    if (startState.session.currentSessionId !== uuid) return
    if (background && startState.conversationLoad.phase !== 'idle') return
    const navigationRequestId = startState.conversationLoad.requestId
    const navigationGeneration = conversationNavigationGeneration
    // Immediate skeleton: the pane reacts to the click, not to the network.
    if (!background) dispatch(chatActions.setSessionLoading(true))
    try {
      const entries = await api.session(uuid)
      const state = getState() as RootState
      if (
        state.session.currentSessionId !== uuid ||
        conversationNavigationGeneration !== navigationGeneration ||
        state.conversationLoad.requestId !== navigationRequestId ||
        state.conversationLoad.phase !== 'idle'
      ) return
      const running = !!state.session.tasks.find((task) => task.uuid === uuid)?.running
      dispatch(chatActions.clearChat())
      dispatch(chatActions.setTimeline(replayTimeline(entries, running)))
      dispatch(chatActions.setRunning(running))
    } finally {
      if (!background) dispatch(chatActions.setSessionLoading(false))
    }
  },
)

/**
 * Start a fresh chat: clear the timeline, switch to the chat view, and ask the
 * backend for a new session id. Shared by the Sidebar "new task" button and the
 * ⌘N / ⇧⌘O keyboard shortcuts. The empty session stays out of the sidebar until
 * the first user message (backend only indexes then).
 */
interface ProvisionNewChatOptions {
  workspaceKind?: WorkspaceKind
  projectPath?: string
  surfaceError?: boolean
  resetBeforeProvision?: boolean
  expectedSessionId?: string
  requireIdle?: boolean
}

async function provisionNewChat(
  dispatch: AppDispatch,
  getState: () => RootState,
  options: ProvisionNewChatOptions = {},
) {
  await dispatch(cancelConversationLoad())
  // Re-check immediately after the async cancellation barrier. A WS agent_start
  // or foreground navigation can land while cancellation yields; nothing below
  // awaits before the reset, so a passing check now owns the atomic clear.
  const current = getState()
  if (options.expectedSessionId !== undefined && current.session.currentSessionId !== options.expectedSessionId) return
  if (options.requireIdle && current.chat.isRunning) return
  const guarded = options.expectedSessionId !== undefined || !!options.requireIdle
  const expectedSessionId = options.expectedSessionId ?? current.session.currentSessionId
  const navigationGeneration = conversationNavigationGeneration
  const workspaceKind = options.workspaceKind || current.session.workspaceKind
  const projectPath = workspaceKind === 'scratch'
    ? undefined
    : options.projectPath || current.session.projectPath || undefined
  dispatch(uiActions.setView('chat'))
  if (options.resetBeforeProvision && !guarded) {
    dispatch(chatActions.clearChat())
    dispatch(sessionActions.setCurrentSession(''))
  }
  try {
    const resp = guarded
      ? await api.newSession(
          undefined,
          undefined,
          workspaceKind,
          projectPath,
          { expectedSessionId, requireIdle: !!options.requireIdle },
        )
      : await api.newSession(undefined, undefined, workspaceKind, projectPath)
    const latest = getState()
    if (
      guarded && (
        conversationNavigationGeneration !== navigationGeneration ||
        latest.session.currentSessionId !== expectedSessionId ||
        (!!options.requireIdle && latest.chat.isRunning)
      )
    ) return
    dispatch(chatActions.clearChat())
    dispatch(sessionActions.setCurrentSession(''))
    dispatch(sessionActions.setCurrentSession(resp.session_id))
    dispatch(sessionActions.setProjectPath(resp.project || resp.workspace_key || resp.pwd || getState().session.projectPath))
    dispatch(sessionActions.setWorkspaceKind(resp.workspace_kind || workspaceKind))
    if (resp.provider !== undefined) dispatch(modelActions.setProvider(resp.provider))
    if (resp.model !== undefined) dispatch(modelActions.setModel(resp.model))
    if (resp.agent !== undefined) dispatch(modelActions.setAgent(resp.agent))
    if (resp.mode !== undefined) dispatch(modelActions.setMode(normalizeMode(resp.mode)))
  } catch (error) {
    const latest = getState()
    const stillOwnsUI = !guarded || (
      conversationNavigationGeneration === navigationGeneration &&
      latest.session.currentSessionId === expectedSessionId &&
      (!options.requireIdle || !latest.chat.isRunning)
    )
    if (!stillOwnsUI) return
    if (options.resetBeforeProvision) {
      dispatch(chatActions.clearChat())
      dispatch(sessionActions.setCurrentSession(''))
    }
    if (options.surfaceError) throw error
    // Existing global-new-task behavior stays quiet; health/gate reconciles.
  }
}

export const startNewChat = createAsyncThunk('session/startNew', async (_, { dispatch, getState }) => {
  await provisionNewChat(dispatch as AppDispatch, () => getState() as RootState)
})

/** Provision a fail-closed fresh task for a cold browser open against an
 * already-used sidecar, or for an idle Desktop window being reopened. Unlike
 * the ordinary New Task action, failure must surface: an empty UI must never
 * remain pointed at the backend's old active conversation. */
export const startFreshChat = createAsyncThunk(
  'session/startFresh',
  async (input: {
    projectPath?: string
    workspaceKind?: WorkspaceKind
    expectedSessionId?: string
    requireIdle?: boolean
  } | void, { dispatch, getState }) => {
    await provisionNewChat(dispatch as AppDispatch, () => getState() as RootState, {
      projectPath: input?.projectPath,
      workspaceKind: input?.workspaceKind,
      surfaceError: true,
      resetBeforeProvision: true,
      expectedSessionId: input?.expectedSessionId,
      requireIdle: input?.requireIdle,
    })
  },
)

export const startScratchChat = createAsyncThunk('session/startScratch', async (_, { dispatch, getState }) => {
  await provisionNewChat(dispatch as AppDispatch, () => getState() as RootState, {
    workspaceKind: 'scratch',
    surfaceError: true,
  })
})

export const replaySession = createAsyncThunk(
  'session/replay',
  async (uuid: string, { dispatch }) => {
    let entries: SessionEntry[]
    try {
      entries = await api.session(uuid)
    } catch (e) {
      dispatch(chatActions.clearChat())
      dispatch(chatActions.addMessage({
        role: 'system',
        content: e instanceof Error ? e.message : 'Failed to load session replay',
        level: 'error',
      }))
      return
    }

    dispatch(chatActions.clearChat())
    const timeline = replayTimeline(entries, false)
    dispatch(chatActions.setTimeline(timeline))
    dispatch(chatActions.setRunning(false))
  },
)

function ts(t?: string): number {
  return t ? new Date(t).getTime() : Date.now()
}

export const store = configureStore({
  reducer: {
    chat: chatSlice.reducer,
    session: sessionSlice.reducer,
    conversationLoad: conversationLoadSlice.reducer,
    remoteConnection: remoteConnectionSlice.reducer,
    modelRetry: modelRetrySlice.reducer,
    model: modelSlice.reducer,
    ui: uiSlice.reducer,
  },
})

export type RootState = ReturnType<typeof store.getState>
export type AppDispatch = typeof store.dispatch
