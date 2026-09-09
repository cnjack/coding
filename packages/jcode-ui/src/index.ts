/**
 * jcode-ui — styled React AI chat components, token-driven, backend-agnostic.
 *
 * Quick start:
 *
 *   import { RuntimeProvider, createExternalStoreRuntime, Thread, ChatInput } from 'jcode-ui'
 *   import 'jcode-ui/styles.css'
 *
 *   const runtime = createExternalStoreRuntime({ store, select, actions })
 *
 *   <RuntimeProvider runtime={runtime}>
 *     <Thread />
 *     <ChatInput />
 *   </RuntimeProvider>
 *
 * For custom tool renderers, see `jcode-ui/tool-renderers`.
 */

// Runtime + provider (re-exported from core for convenience).
export {
  RuntimeProvider,
  useRuntimeState,
  useRuntimeSelector,
  useRuntimeActions,
  createExternalStoreRuntime,
  createMockRuntime,
} from 'jcode-ui-core/runtime'

// Registry context + default registry builder.
export { ToolRegistryProvider, useToolRegistry, createDefaultToolRegistry } from './components/ToolRegistryContext.js'

// API base context (for browser_screenshot image resolution).
export { ApiBaseContext, ApiBaseProvider } from './lib/apiBaseContext.js'

// Styled components.
export { Thread } from './components/Thread.js'
export { Message } from './components/Message.js'
export { BranchPicker } from './components/BranchPicker.js'
export { ConnectionBanner } from './components/ConnectionBanner.js'
export { ToolCallCard } from './components/ToolCallCard.js'
export { ActivityGroupCard } from './components/ActivityGroupCard.js'
export type { ActivityGroupCardProps } from './components/ActivityGroupCard.js'
export { ToolRow, ToolRowHeader } from './components/ToolRow.js'
export type { ToolRowProps } from './components/ToolRow.js'
// Deprecated: superseded by ActivityGroupCard — kept for external consumers.
export { ExploringGroupCard } from './components/ExploringGroupCard.js'
export { ToolBatchGroupCard } from './components/ToolBatchGroup.js'
export type { ToolBatchGroupCardProps } from './components/ToolBatchGroup.js'
export { TurnChangesCard } from './components/TurnChangesCard.js'
export type { TurnChangesCardProps } from './components/TurnChangesCard.js'
export { CompletedTurnCard } from './components/CompletedTurnCard.js'
export type { CompletedTurnCardProps } from './components/CompletedTurnCard.js'
export { CompactToolRow } from './components/CompactToolRow.js'
export { ApprovalBanner } from './components/ApprovalBanner.js'
export { AskUserCard } from './components/AskUserCard.js'
export { ContextBar } from './components/ContextBar.js'
export { ChatInput } from './components/ChatInput.js'
export { ModelSelector } from './components/ModelSelector.js'
export type { ModelSelectorOption, ModelSelectorProps } from './components/ModelSelector.js'
export { Reasoning } from './components/Reasoning.js'
export { Sources } from './components/Sources.js'
export { Attachment, AttachmentList, PendingAttachmentList } from './components/Attachment.js'
export { ThreadList, formatRelative } from './components/ThreadList.js'
export { ThreadWelcome } from './components/ThreadWelcome.js'
export { Suggestions } from './components/Suggestions.js'
export type { SuggestionItem, SuggestionsProps } from './components/Suggestions.js'
export { ExportButton } from './components/ExportButton.js'
export { QuoteSelection, formatQuote } from './components/QuoteSelection.js'
export type { QuoteSelectionProps } from './components/QuoteSelection.js'
export type { ToolCallCardProps, ToolCallCardSlots } from './components/ToolCallCard.js'
export { TaskList, RuntimeTaskList } from './components/TaskList.js'
export type {
  TaskListProps,
  TaskListItemProps,
  RuntimeTaskListProps,
} from './components/TaskList.js'
export { Artifact } from './components/Artifact.js'
export type { ArtifactProps } from './components/Artifact.js'
export { GeneratedImageCard } from './components/GeneratedImageCard.js'
export type {
  GeneratedImageCardProps,
  GeneratedImageCardStrings,
  GeneratedImageState,
} from './components/GeneratedImageCard.js'

// Markdown pipeline (same renderer Message uses).
export { renderMarkdown } from './lib/markdown.js'

// Unified copy-target model + clipboard helper (shared with the TUI /copy
// picker semantics: full response / code block / blockquote).
export { analyzeCopyTargets } from './lib/copyTargets.js'
export type { CopyTarget, CopyTargetKind } from './lib/copyTargets.js'
export { copyText } from './lib/clipboard.js'

// Re-export the core types + primitives so consumers have a single import.
export type {
  Message as MessageData,
  ToolCall,
  Approval,
  ThreadItem,
  TokenSnapshot,
  TaskContextBreakdown,
  AskUserQuestion,
  AskUserAnswer,
  TodoItem,
  Goal,
  Role,
  ActivityGroup,
  CompletedTurn,
  ToolBatchGroup,
  ExploringGroup,
  TurnChangesSummary,
  TurnFileChange,
  ArtifactRef,
  ToolPhase,
  ToolOutcome,
  ToolSurface,
} from 'jcode-ui-core'
export {
  isMessageItem,
  isToolItem,
  isApprovalItem,
  isActivityItem,
  isTurnItem,
  isExploringItem,
  isBatchItem,
  isTurnChangesItem,
  groupActivityTimeline,
  bindApprovalsToTools,
  groupCompletedTurns,
  summarizeActivityCounts,
  countActivityFlags,
  groupExploringTimeline,
  groupToolTimeline,
  isCollapsibleTool,
  summarizeExploringSteps,
  summarizeExploringCounts,
  summarizeTurnChanges,
  appendTurnChangeSummaries,
  diffStatForTool,
  useElapsed,
  formatElapsed,
} from 'jcode-ui-core'
