import { memo, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import {
  ArrowDownTrayIcon,
  ArrowTopRightOnSquareIcon,
  Cog6ToothIcon,
  FolderOpenIcon,
  PhotoIcon,
} from '@heroicons/react/24/outline'
import type { ArtifactRef, ToolOutcome, ToolPhase } from 'jcode-ui-core'

export type GeneratedImageState = Exclude<ToolPhase, 'terminal'> | ToolOutcome

export interface GeneratedImageCardStrings {
  queued: string
  queuedHint: string
  generating: string
  saving: string
  savingHint: string
  succeeded: string
  failed: string
  uncertain: string
  uncertainHint: string
  cancelled: string
  cancelledHint: string
  authError: string
  quotaError: string
  safetyError: string
  rateLimitError: string
  downloadError: string
  persistError: string
  genericError: string
  loadingAsset: string
  assetError: string
  download: string
  openImage: string
  openArtifact: string
  reveal: string
  openSettings: string
}

const DEFAULT_STRINGS: GeneratedImageCardStrings = {
  queued: 'Preparing image request',
  queuedHint: 'No provider request has been sent.',
  generating: 'Generating',
  saving: 'Saving',
  savingHint: 'The provider returned an image. JCode is saving it.',
  succeeded: 'Image generated',
  failed: 'Image generation failed',
  uncertain: 'Status unknown',
  uncertainHint: 'The provider may have accepted the request and a charge may apply.',
  cancelled: 'Stopped',
  cancelledHint: 'No new request will be sent from this card.',
  authError: 'Authentication failed. Check this provider configuration.',
  quotaError: 'The provider reported that its quota is exhausted.',
  safetyError: 'The provider blocked this request under its safety policy.',
  rateLimitError: 'The provider rate limit was reached.',
  downloadError: 'JCode could not safely verify the returned image.',
  persistError: 'The provider may have completed the request, but JCode could not save the image. A charge may apply.',
  genericError: 'The request did not produce a saved image.',
  loadingAsset: 'Loading saved image',
  assetError: 'The saved image could not be loaded.',
  download: 'Download image',
  openImage: 'Open image in a new window',
  openArtifact: 'Open Artifact',
  reveal: 'Reveal in folder',
  openSettings: 'Open provider settings',
}

const SCAN_WEAVE_RAILS = Array.from({ length: 8 }, (_, index) => index)
const SCAN_WEAVE_NODES = [
  [1, 1],
  [3, 2],
  [5, 1],
  [6, 4],
  [2, 5],
  [4, 6],
] as const

export interface GeneratedImageCardProps {
  state: GeneratedImageState
  provider?: string
  model?: string
  aspectRatio?: string
  startedAt?: number
  imageSrc?: string
  title?: string
  alt?: string
  artifact?: ArtifactRef
  errorCode?: string
  errorMessage?: string
  assetError?: string
  strings?: Partial<GeneratedImageCardStrings>
  onOpenImage?: () => void
  onDownload?: () => void
  onOpenArtifact?: () => void
  onReveal?: () => void
  onOpenSettings?: () => void
}

export const GeneratedImageCard = memo(function GeneratedImageCard({
  state,
  provider,
  model,
  aspectRatio,
  imageSrc,
  title,
  alt,
  artifact,
  errorCode,
  errorMessage,
  assetError,
  strings: stringOverrides,
  onOpenImage,
  onDownload,
  onOpenArtifact,
  onReveal,
  onOpenSettings,
}: GeneratedImageCardProps) {
  const strings = useMemo(() => ({ ...DEFAULT_STRINGS, ...stringOverrides }), [stringOverrides])
  const [imageReady, setImageReady] = useState(false)
  const [imageFailed, setImageFailed] = useState(false)
  const assetRef = useRef<HTMLImageElement | null>(null)

  useEffect(() => {
    setImageReady(false)
    setImageFailed(false)
  }, [imageSrc])

  const ratio = safeAspectRatio(aspectRatio, artifact)
  const isSuccess = state === 'succeeded'
  const isPreviewing = state === 'generating' || state === 'saving'
  const isBusy = state === 'queued' || state === 'generating' || state === 'saving'
  const label = stateLabel(state, strings)
  const hint = stateHint(state, strings, errorCode, errorMessage)
  const modelLine = [provider, model].filter(Boolean).join(' · ')
  const metadata = artifactMetadata(artifact)
  const canOpenSettings = !!onOpenSettings && isSettingsError(errorCode)
  const visibleAssetError = assetError || (imageFailed ? strings.assetError : '')
  const isLoadingAsset = isSuccess && !visibleAssetError && (!imageSrc || !imageReady)
  const cardStyle = {
    aspectRatio: String(ratio),
    '--jcode-generated-image-width': imageWidthForRatio(ratio),
    ...(isPreviewing
      ? { '--jcode-generated-image-preview-width': previewWidthForRatio(ratio) }
      : {}),
  } as CSSProperties

  // The delegated React onLoad can silently miss a load that completes around
  // commit (cached/blob assets, event-delegation quirks) and the card then
  // sticks on "loading" forever. Listen on the node itself so every load is
  // delivered, and settle immediately for an asset that is already decoded.
  useEffect(() => {
    const el = assetRef.current
    if (!isSuccess || !imageSrc || !el) return
    const reveal = () => revealWhenDecoded(el, imageSrc, () => setImageReady(true))
    el.addEventListener('load', reveal)
    if (el.complete && el.naturalWidth > 0) reveal()
    return () => { el.removeEventListener('load', reveal) }
  }, [imageSrc, isSuccess])

  return (
    <>
      <section
        data-jcode-ui=""
        data-generated-image-state={state}
        className={`jcode-generated-image${imageReady ? ' is-image-ready' : ''}`}
        style={cardStyle}
        aria-label={title || label}
        aria-busy={isBusy || isLoadingAsset}
      >
        {isSuccess && imageSrc ? (
          <img
            ref={assetRef}
            className="jcode-generated-image__asset"
            src={imageSrc}
            alt={alt || title || strings.succeeded}
            onLoad={(event) => revealWhenDecoded(event.currentTarget, imageSrc, () => setImageReady(true))}
            onError={() => setImageFailed(true)}
          />
        ) : null}

        {isSuccess && imageReady && onOpenImage ? (
          <button
            type="button"
            className="jcode-generated-image__preview-trigger"
            aria-label={strings.openImage}
            title={strings.openImage}
            onClick={onOpenImage}
          />
        ) : null}

        {isBusy || (isSuccess && (!imageSrc || !imageReady)) ? (
          <div className="jcode-generated-image__field" aria-hidden="true" />
        ) : null}

        {isPreviewing ? (
          <div className="jcode-generated-image__motion" aria-hidden="true">
            <span className="jcode-generated-image__scan-weave">
              {SCAN_WEAVE_RAILS.map((rail) => (
                <span
                  key={`x-${rail}`}
                  className="jcode-generated-image__weave-rail is-horizontal"
                  style={weaveRailStyle(rail, 'horizontal')}
                />
              ))}
              {SCAN_WEAVE_RAILS.map((rail) => (
                <span
                  key={`y-${rail}`}
                  className="jcode-generated-image__weave-rail is-vertical"
                  style={weaveRailStyle(rail, 'vertical')}
                />
              ))}
              {SCAN_WEAVE_NODES.map(([x, y]) => (
                <span
                  key={`${x}-${y}`}
                  className={`jcode-generated-image__weave-node${state === 'saving' ? ' is-settling' : ''}`}
                  style={weaveNodeStyle(x, y)}
                />
              ))}
            </span>
          </div>
        ) : (
          <div className="jcode-generated-image__status">
            <span className="jcode-generated-image__status-icon" aria-hidden="true">
              <PhotoIcon />
            </span>
            <span className="jcode-generated-image__status-copy">
              <span className="jcode-generated-image__status-line" role="status" aria-live="polite">
                {isLoadingAsset ? strings.loadingAsset : label}
              </span>
              {modelLine ? <span className="jcode-generated-image__model">{modelLine}</span> : null}
              {hint ? <span className="jcode-generated-image__hint">{hint}</span> : null}
              {isSuccess && visibleAssetError ? <span className="jcode-generated-image__hint is-error">{visibleAssetError}</span> : null}
            </span>
          </div>
        )}

        {isSuccess && metadata ? <div className="jcode-generated-image__metadata">{metadata}</div> : null}

        {isSuccess && (onDownload || onOpenArtifact || onReveal) ? (
          <div className="jcode-generated-image__actions">
            {onDownload ? (
              <ImageAction label={strings.download} onClick={onDownload}><ArrowDownTrayIcon /></ImageAction>
            ) : null}
            {onOpenArtifact ? (
              <ImageAction label={strings.openArtifact} onClick={onOpenArtifact}><ArrowTopRightOnSquareIcon /></ImageAction>
            ) : null}
            {onReveal ? (
              <ImageAction label={strings.reveal} onClick={onReveal}><FolderOpenIcon /></ImageAction>
            ) : null}
          </div>
        ) : null}

        {canOpenSettings ? (
          <button type="button" className="jcode-generated-image__settings" onClick={onOpenSettings}>
            <Cog6ToothIcon aria-hidden="true" />
            <span>{strings.openSettings}</span>
          </button>
        ) : null}
      </section>
      {isPreviewing ? (
        <span
          className="jcode-generated-image__sr-status"
          role="status"
          aria-live="polite"
          aria-atomic="true"
        >
          {label}
        </span>
      ) : null}
    </>
  )
})

function revealWhenDecoded(image: HTMLImageElement, expectedSrc: string, reveal: () => void): void {
  const settle = () => {
    // A late decode must not reveal an asset whose src has since changed.
    if (image.getAttribute('src') === expectedSrc) reveal()
  }
  if (typeof image.decode !== 'function') {
    settle()
    return
  }
  void image.decode().catch(() => undefined).then(settle)
}

function ImageAction({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" className="jcode-generated-image__action" aria-label={label} title={label} onClick={onClick}>
      {children}
    </button>
  )
}

function safeAspectRatio(value?: string, artifact?: ArtifactRef): number {
  if (artifact?.width && artifact.height) return clampRatio(artifact.width / artifact.height)
  const match = value?.trim().match(/^(\d{1,4})\s*[:/x]\s*(\d{1,4})$/i)
  if (!match) return 1
  const width = Number(match[1])
  const height = Number(match[2])
  return height > 0 ? clampRatio(width / height) : 1
}

function clampRatio(value: number): number {
  return Number.isFinite(value) ? Math.min(4, Math.max(0.25, value)) : 1
}

function imageWidthForRatio(ratio: number): string {
  // Keep completed images within the same compact visual scale as previews,
  // while bounding very tall images to 22rem.
  return `${Number(Math.min(18, ratio * 22).toFixed(3))}rem`
}

function previewWidthForRatio(ratio: number): string {
  if (ratio < 0.8) return '12rem'
  if (ratio > 1.25) return '18rem'
  return '16rem'
}

function weaveRailStyle(index: number, direction: 'horizontal' | 'vertical'): CSSProperties {
  return {
    '--jcode-generated-weave-position': `${Number((((index + 1) / 9) * 100).toFixed(3))}%`,
    '--jcode-generated-weave-delay': `${index * -90 - (direction === 'vertical' ? 420 : 0)}ms`,
  } as CSSProperties
}

function weaveNodeStyle(x: number, y: number): CSSProperties {
  return {
    '--jcode-generated-weave-x': `${Number((((x + 1) / 9) * 100).toFixed(3))}%`,
    '--jcode-generated-weave-y': `${Number((((y + 1) / 9) * 100).toFixed(3))}%`,
    '--jcode-generated-weave-delay': `${(x + y) * -80}ms`,
  } as CSSProperties
}

function stateLabel(state: GeneratedImageState, strings: GeneratedImageCardStrings): string {
  return strings[state]
}

function stateHint(
  state: GeneratedImageState,
  strings: GeneratedImageCardStrings,
  errorCode?: string,
  errorMessage?: string,
): string {
  if (state === 'queued') return strings.queuedHint
  if (state === 'saving') return strings.savingHint
  if (state === 'uncertain') return typedErrorHint(errorCode, strings) || strings.uncertainHint
  if (state === 'cancelled') return strings.cancelledHint
  if (state !== 'failed') return ''
  return typedErrorHint(errorCode, strings) || errorMessage || strings.genericError
}

function typedErrorHint(errorCode: string | undefined, strings: GeneratedImageCardStrings): string {
  switch (errorCode) {
    case 'auth':
    case 'authentication_failed':
    case 'invalid_api_key':
      return strings.authError
    case 'quota':
    case 'quota_exceeded':
      return strings.quotaError
    case 'safety':
    case 'safety_blocked':
      return strings.safetyError
    case 'rate_limit':
    case 'rate_limited':
      return strings.rateLimitError
    case 'download_failed':
    case 'asset_download_failed':
    case 'asset_host_blocked':
    case 'invalid_image':
    case 'invalid_provider_output':
      return strings.downloadError
    case 'persist_failed':
    case 'artifact_persist_failed':
    case 'journal_persist_failed':
      return strings.persistError
    default:
      return ''
  }
}

function isSettingsError(errorCode?: string): boolean {
  return errorCode === 'auth' || errorCode === 'authentication_failed' || errorCode === 'invalid_api_key'
}

function artifactMetadata(artifact?: ArtifactRef): string {
  if (!artifact) return ''
  const format = artifact.media_type.split('/')[1]?.toUpperCase() || artifact.media_type
  const dimensions = artifact.width && artifact.height ? `${artifact.width}×${artifact.height}` : ''
  const bytes = formatBytes(artifact.size)
  return [format, dimensions, bytes].filter(Boolean).join(' · ')
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}
