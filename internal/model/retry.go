package model

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cnjack/jcode/internal/config"
	openai "github.com/sashabaranov/go-openai"
)

// DefaultMaxRetries is shared by the agent retry configuration and retry-status
// observers so every transport reports the same bounded attempt count.
const DefaultMaxRetries = 5

// ---------------------------------------------------------------------------
// Error Classification
// ---------------------------------------------------------------------------

// APIErrorCategory classifies LLM API errors into actionable categories.
type APIErrorCategory int

const (
	// ErrCategoryTransient — network blips, timeouts, 5xx; safe to retry.
	ErrCategoryTransient APIErrorCategory = iota
	// ErrCategoryRateLimit — 429 / "overloaded"; retry with back-off.
	ErrCategoryRateLimit
	// ErrCategoryContextOverflow — input too long; needs compaction, NOT retry.
	ErrCategoryContextOverflow
	// ErrCategoryAuth — 401/403; permanent until key is fixed.
	ErrCategoryAuth
	// ErrCategoryQuota — 402; the account is out of credit or the plan does not
	// cover this model. Distinct from ErrCategoryRateLimit because waiting does
	// not help: a rate limit clears on its own, a spent quota never does. Retrying
	// a 402 just burns the turn and then reports something misleading.
	ErrCategoryQuota
	// ErrCategoryFatal — 400 bad request, unknown; do not retry.
	ErrCategoryFatal
)

func (c APIErrorCategory) String() string {
	switch c {
	case ErrCategoryTransient:
		return "transient"
	case ErrCategoryRateLimit:
		return "rate_limit"
	case ErrCategoryContextOverflow:
		return "context_overflow"
	case ErrCategoryAuth:
		return "auth"
	case ErrCategoryQuota:
		return "quota"
	case ErrCategoryFatal:
		return "fatal"
	default:
		return "unknown"
	}
}

// contextOverflowPatterns matches error messages from various LLM providers
// that indicate the input exceeded the model's context window.
// Sourced from Claude-Code & OpenCode's 40+ patterns.
var contextOverflowPatterns = []*regexp.Regexp{
	// OpenAI-compatible
	regexp.MustCompile(`(?i)context.length.exceeded`),
	regexp.MustCompile(`(?i)maximum context length`),
	regexp.MustCompile(`(?i)max.tokens.exceed.*context`),
	regexp.MustCompile(`(?i)input.*too long`),
	regexp.MustCompile(`(?i)prompt is too long`),
	regexp.MustCompile(`(?i)token limit`),
	regexp.MustCompile(`(?i)request too large`),
	regexp.MustCompile(`(?i)exceeds? the model.s.*(?:token|context|input)`),
	// Anthropic
	regexp.MustCompile(`(?i)prompt is too long:\s*\d+\s*tokens?\s*>\s*\d+`),
	regexp.MustCompile(`(?i)input length and.*max_tokens.*exceed.*context limit`),
	// Google / Gemini
	regexp.MustCompile(`(?i)exceeds? the maximum number of tokens`),
	regexp.MustCompile(`(?i)exceeds? the maximum input token`),
	// Generic
	regexp.MustCompile(`(?i)content.*too.*large`),
	regexp.MustCompile(`(?i)payload.*too.*large`),
}

// rateLimitPatterns matches error messages indicating rate-limiting.
var rateLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)rate.limit`),
	regexp.MustCompile(`(?i)too many requests`),
	regexp.MustCompile(`(?i)overloaded`),
	regexp.MustCompile(`(?i)resource.exhausted`),
	regexp.MustCompile(`(?i)quota.exceeded`),
	regexp.MustCompile(`(?i)throttl`),
}

// quotaPatterns match messages that mean "you are out of money/credit", as
// opposed to "you are going too fast". Providers are wildly inconsistent here —
// several return 400 or 403 with a billing message rather than 402 — so the text
// has to be matched, not just the status.
var quotaPatterns = []*regexp.Regexp{
	// Alibaba uses 403 when the account has not enabled the selected model.
	regexp.MustCompile(`(?i)AccessDenied\.Unpurchased`),
	regexp.MustCompile(`(?i)payment.required`),
	regexp.MustCompile(`(?i)insufficient.(balance|credit|quota|funds)`),
	regexp.MustCompile(`(?i)(quota|credit|balance).*(exhaust|depleted|run out|used up)`),
	regexp.MustCompile(`(?i)free.trial.*(exhaust|expired|ended)`),
	regexp.MustCompile(`(?i)billing.*(not enabled|required|disabled)`),
	regexp.MustCompile(`(?i)exceeded your current quota`), // OpenAI
	regexp.MustCompile(`(?i)arrearage|owe|unpaid`),
	regexp.MustCompile(`(?i)账户余额不足|欠费|额度.*(用尽|耗尽|不足)`),
	// Moonshot: "You've reached your usage limit for this billing cycle. Your
	// quota will be refreshed in the next cycle. To continue now, purchase extra
	// usage or upgrade your plan". Observed live, on a 403 — and it matched none
	// of the patterns above, so it was classified as auth and the user was told
	// to check an API key that was perfectly fine. The lesson generalizes: a
	// provider's *sentiment* here ("you are out") is far more stable than its
	// vocabulary, so match on several phrasings rather than one house style.
	regexp.MustCompile(`(?i)(reached|hit).{0,20}usage.{0,10}limit`),
	regexp.MustCompile(`(?i)purchase.{0,20}(extra|additional).{0,10}usage`),
	regexp.MustCompile(`(?i)(usage|plan).{0,10}limit.{0,30}billing cycle`),
	// NOT "upgrade your plan" on its own: rate-limit copy says it too ("upgrade
	// your plan for higher rate limits"), and misreading a rate limit as a spent
	// quota means not retrying something that would have worked in 20 seconds.
	// The phrase only carries meaning next to a usage/billing word, which the
	// patterns above already require.
	regexp.MustCompile(`(?i)out of (credit|quota|balance)`),
	regexp.MustCompile(`(?i)(用量|用量额度|配额).{0,10}(已达|超出|用尽)`),
}

// ClassifyError determines the category of an API error.
func unpurchasedModel(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *openai.APIError
	if asAPIErr(err, &apiErr) && strings.EqualFold(fmt.Sprint(apiErr.Code), "AccessDenied.Unpurchased") {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "accessdenied.unpurchased")
}

func ClassifyError(err error) APIErrorCategory {
	if err == nil {
		return ErrCategoryFatal
	}

	if unpurchasedModel(err) {
		return ErrCategoryQuota
	}
	msg := err.Error()

	// Check OpenAI SDK typed errors first.
	var apiErr *openai.APIError
	if asAPIErr(err, &apiErr) {
		return classifyByStatus(apiErr.HTTPStatusCode, msg)
	}

	// Network-level errors are always transient.
	var netErr net.Error
	if asNetErr(err, &netErr) {
		return ErrCategoryTransient
	}

	// Fall back to message text matching.
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded") {
		return ErrCategoryTransient
	}
	if strings.Contains(lower, "connection refused") || strings.Contains(lower, "connection reset") {
		return ErrCategoryTransient
	}

	for _, re := range contextOverflowPatterns {
		if re.MatchString(msg) {
			return ErrCategoryContextOverflow
		}
	}
	// Quota is checked before rate limit: several providers word an exhausted
	// balance in language that also trips the rate-limit patterns, and the two
	// need opposite handling (back off vs. stop and tell the user).
	for _, re := range quotaPatterns {
		if re.MatchString(msg) {
			return ErrCategoryQuota
		}
	}
	for _, re := range rateLimitPatterns {
		if re.MatchString(msg) {
			return ErrCategoryRateLimit
		}
	}

	return ErrCategoryFatal
}

func classifyByStatus(status int, msg string) APIErrorCategory {
	switch {
	case status == 402:
		return ErrCategoryQuota
	case status == 429:
		// A 429 whose body is about money, not pace: some gateways return 429
		// when a prepaid balance hits zero. Backing off would never clear it.
		for _, re := range quotaPatterns {
			if re.MatchString(msg) {
				return ErrCategoryQuota
			}
		}
		return ErrCategoryRateLimit
	case status == 529:
		return ErrCategoryRateLimit // Anthropic "overloaded"
	case status == 401 || status == 403:
		// 403 is where several providers put billing failures.
		for _, re := range quotaPatterns {
			if re.MatchString(msg) {
				return ErrCategoryQuota
			}
		}
		return ErrCategoryAuth
	case status == 408 || status == 409:
		return ErrCategoryTransient
	case status >= 500 && status < 600:
		return ErrCategoryTransient
	case status == 413:
		return ErrCategoryContextOverflow
	case status == 400:
		// 400 may contain context overflow info.
		for _, re := range contextOverflowPatterns {
			if re.MatchString(msg) {
				return ErrCategoryContextOverflow
			}
		}
		return ErrCategoryFatal
	default:
		return ErrCategoryFatal
	}
}

// ---------------------------------------------------------------------------
// Retry-After Header Parsing
// ---------------------------------------------------------------------------

// ParseRetryAfter extracts a delay from an error message or OpenAI APIError.
// It looks for Retry-After header patterns in the error text.
// Returns 0 if no delay information is found.
func ParseRetryAfter(err error) time.Duration {
	var apiErr *openai.APIError
	if asAPIErr(err, &apiErr) {
		return parseRetryAfterFromAPIError(apiErr)
	}
	return 0
}

func parseRetryAfterFromAPIError(apiErr *openai.APIError) time.Duration {
	if apiErr == nil {
		return 0
	}
	// The go-openai library includes the header value in the error message
	// like "Rate limit reached... Please retry after Xs" or similar.
	msg := apiErr.Message
	return parseRetryAfterFromMessage(msg)
}

// parseRetryAfterFromMessage tries to extract a retry delay from error text.
// Supports patterns like:
//
//	"Please retry after 30s"
//	"try again in 30 seconds"
//	"retry after 2.5 seconds"
//	"Retry-After: 30"
var retryAfterSecsPattern = regexp.MustCompile(
	`(?i)(?:retry.after|try.again.in|please.wait)\s*[:=]?\s*(\d+(?:\.\d+)?)\s*(?:s|sec|seconds?)?\b`,
)

var retryAfterMsPattern = regexp.MustCompile(
	`(?i)retry.after.ms\s*[:=]?\s*(\d+)`,
)

func parseRetryAfterFromMessage(msg string) time.Duration {
	// Check millisecond header first (higher precision).
	if m := retryAfterMsPattern.FindStringSubmatch(msg); len(m) > 1 {
		if ms, err := strconv.ParseInt(m[1], 10, 64); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	// Check seconds-based pattern.
	if m := retryAfterSecsPattern.FindStringSubmatch(msg); len(m) > 1 {
		if secs, err := strconv.ParseFloat(m[1], 64); err == nil && secs > 0 {
			return time.Duration(secs * float64(time.Second))
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// Retry Decision & Backoff (for Eino ModelRetryConfig)
// ---------------------------------------------------------------------------

const (
	baseDelay      = 500 * time.Millisecond
	maxDelay       = 32 * time.Second
	maxRetryAfter  = 5 * time.Minute // cap server-suggested delay
	jitterFraction = 0.25            // 0-25% random jitter
)

// IsRetryable returns true if the error is safe to retry. The shared Eino
// decision callback uses it after inspecting whether a stream emitted output.
//
// Context overflow errors are NOT retryable — they need compaction.
// Auth errors are NOT retryable — they need user action.
// Quota errors are NOT retryable — a spent balance does not refill on a backoff
// timer, so retrying only delays telling the user the one thing they can act on.
func IsRetryable(ctx context.Context, err error) bool {
	cat := ClassifyError(err)
	switch cat {
	case ErrCategoryTransient, ErrCategoryRateLimit:
		if observer := RetryObserverFromContext(ctx); observer != nil {
			observer.record(err)
		}
		return true
	default:
		return false
	}
}

// ShouldRetryModelCall applies the shared retry classification with one
// important streaming guard: once a model has emitted any output, accepting a
// framework retry would append a second attempt after an already-visible prefix
// on append-only transports. Preserve that partial attempt and surface its
// error instead; retries remain enabled for failures that happen before output.
func ShouldRetryModelCall(ctx context.Context, retryCtx *adk.RetryContext) *adk.RetryDecision {
	if retryCtx == nil || !IsRetryable(ctx, retryCtx.Err) {
		return &adk.RetryDecision{}
	}
	if retryCtx.OutputMessage != nil {
		return &adk.RetryDecision{}
	}
	return &adk.RetryDecision{Retry: true}
}

// SmartBackoff returns a delay for the given retry attempt, respecting
// server-sent Retry-After hints when available. It is designed to be used
// as ModelRetryConfig.BackoffFunc in the Eino framework.
//
// Strategy (matching Claude-Code & OpenCode patterns):
//  1. If the error contains a Retry-After hint, use it (capped at 5 min).
//  2. Otherwise fall back to exponential backoff: 500ms × 2^(attempt-1),
//     capped at 32s, plus 0-25% random jitter.
func SmartBackoff(ctx context.Context, attempt int) time.Duration {
	return smartBackoff(ctx, attempt, DefaultMaxRetries)
}

// SmartBackoffWithMaxRetries binds the configured retry ceiling to emitted
// status events. Subagents use a lower ceiling than the top-level agent, so the
// UI denominator must travel with the callback that actually performs backoff.
func SmartBackoffWithMaxRetries(maxRetries int) func(context.Context, int) time.Duration {
	return func(ctx context.Context, attempt int) time.Duration {
		return smartBackoff(ctx, attempt, maxRetries)
	}
}

func smartBackoff(ctx context.Context, attempt, maxRetries int) time.Duration {
	observer := RetryObserverFromContext(ctx)
	// Try to extract Retry-After from the most recent error via context.
	// Eino does not pass the error to BackoffFunc, so we rely on the
	// context observer populated by IsRetryable. WithRetryError remains supported
	// for direct callers and older integrations.
	retryErr := retryErrorFromCtx(ctx)
	if retryErr == nil && observer != nil {
		retryErr = observer.lastError()
	}
	delay := time.Duration(0)
	if retryErr != nil {
		if serverDelay := ParseRetryAfter(retryErr); serverDelay > 0 {
			delay = serverDelay
			if delay > maxRetryAfter {
				delay = maxRetryAfter
			}
			config.Logger().Printf("[retry] attempt %d: using server Retry-After %v (capped from %v)", attempt, delay, serverDelay)
		}
	}
	if delay == 0 {
		delay = exponentialBackoff(attempt)
	}
	if observer != nil && retryErr != nil && ClassifyError(retryErr) == ErrCategoryRateLimit {
		observer.publish(RetryBackoffEvent{Attempt: attempt, MaxAttempts: maxRetries, Delay: delay})
	}
	return delay
}

// exponentialBackoff calculates delay: baseDelay × 2^(attempt-1) + jitter,
// capped at maxDelay.
func exponentialBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	exp := math.Min(float64(baseDelay)*math.Pow(2, float64(attempt-1)), float64(maxDelay))
	jitter := exp * jitterFraction * rand.Float64()
	delay := time.Duration(exp + jitter)
	config.Logger().Printf("[retry] attempt %d: exponential backoff %v", attempt, delay)
	return delay
}

// ---------------------------------------------------------------------------
// Context key for passing last error to BackoffFunc (works around Eino API)
// ---------------------------------------------------------------------------

type retryErrorKeyType struct{}

var retryErrorKey retryErrorKeyType

type retryObserverKeyType struct{}

var retryObserverKey retryObserverKeyType

// RetryBackoffEvent is emitted immediately before Eino waits and retries a
// rate-limited model call. Attempt is 1-based and Delay is the actual backoff
// selected after applying Retry-After and caps.
type RetryBackoffEvent struct {
	Attempt     int
	MaxAttempts int
	Delay       time.Duration
}

// RetryObserver keeps retry observability outside the retry decision itself.
// It is safe for the model callback and runner event loop to use concurrently.
type RetryObserver struct {
	mu      sync.Mutex
	lastErr error
	active  bool
	max     int
	notify  func(RetryBackoffEvent)
}

// NewRetryObserver creates a retry observer with an optional event callback.
func NewRetryObserver(notify func(RetryBackoffEvent)) *RetryObserver {
	return &RetryObserver{notify: notify}
}

// WithRetryObserver attaches retry observability to a model request context.
func WithRetryObserver(ctx context.Context, observer *RetryObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, retryObserverKey, observer)
}

// RetryObserverFromContext returns the observer attached to ctx, if any.
func RetryObserverFromContext(ctx context.Context) *RetryObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(retryObserverKey).(*RetryObserver)
	return observer
}

func (o *RetryObserver) record(err error) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.lastErr = err
	o.mu.Unlock()
}

func (o *RetryObserver) lastError() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.lastErr
}

func (o *RetryObserver) publish(event RetryBackoffEvent) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.active = true
	o.max = event.MaxAttempts
	notify := o.notify
	o.mu.Unlock()
	if notify != nil {
		notify(event)
	}
}

// Resolve clears the active marker after the first successful model output.
// It returns the retry ceiling exactly once per cycle so callers can emit a
// correctly bounded recovery transition without duplicate events.
func (o *RetryObserver) Resolve() (resolved bool, maxAttempts int) {
	if o == nil {
		return false, 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active {
		return false, 0
	}
	o.active = false
	return true, o.max
}

// Clear drops an in-flight marker when the turn stops or exhausts retries.
func (o *RetryObserver) Clear() bool {
	return o.clearActive()
}

func (o *RetryObserver) clearActive() bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	wasActive := o.active
	o.active = false
	o.lastErr = nil
	return wasActive
}

// WithRetryError stores an error in context for BackoffFunc to inspect.
func WithRetryError(ctx context.Context, err error) context.Context {
	return context.WithValue(ctx, retryErrorKey, err)
}

func retryErrorFromCtx(ctx context.Context) error {
	if v := ctx.Value(retryErrorKey); v != nil {
		if err, ok := v.(error); ok {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers — type assertions via interface matching (avoids import cycles)
// ---------------------------------------------------------------------------

func asAPIErr(err error, target **openai.APIError) bool {
	// Walk the error chain.
	for err != nil {
		if ae, ok := err.(*openai.APIError); ok {
			*target = ae
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func asNetErr(err error, target *net.Error) bool {
	for err != nil {
		if ne, ok := err.(net.Error); ok {
			*target = ne
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ---------------------------------------------------------------------------
// Context Overflow Info Extraction
// ---------------------------------------------------------------------------

// ContextOverflowInfo holds parsed token counts from an overflow error.
type ContextOverflowInfo struct {
	ActualTokens int
	LimitTokens  int
	TokenGap     int // ActualTokens - LimitTokens
}

var overflowTokenPattern = regexp.MustCompile(
	`(\d+)\s*(?:\+\s*\d+\s*)?>(?:\s*=)?\s*(\d+)`,
)

var promptTooLongPattern = regexp.MustCompile(
	`(?i)(?:prompt is too long|too long)[^0-9]*(\d+)\s*tokens?\s*>\s*(\d+)`,
)

// ParseContextOverflow extracts token counts from a context overflow error.
// Returns nil if the error is not a context overflow or counts cannot be parsed.
func ParseContextOverflow(err error) *ContextOverflowInfo {
	if err == nil {
		return nil
	}
	msg := err.Error()

	// Try "prompt is too long: 137500 tokens > 135000"
	if m := promptTooLongPattern.FindStringSubmatch(msg); len(m) > 2 {
		actual, _ := strconv.Atoi(m[1])
		limit, _ := strconv.Atoi(m[2])
		if actual > 0 && limit > 0 {
			return &ContextOverflowInfo{
				ActualTokens: actual,
				LimitTokens:  limit,
				TokenGap:     actual - limit,
			}
		}
	}

	// Try "input length and max_tokens exceed context limit: 188059 + 20000 > 200000"
	if m := overflowTokenPattern.FindStringSubmatch(msg); len(m) > 2 {
		actual, _ := strconv.Atoi(m[1])
		limit, _ := strconv.Atoi(m[2])
		if actual > 0 && limit > 0 {
			return &ContextOverflowInfo{
				ActualTokens: actual,
				LimitTokens:  limit,
				TokenGap:     actual - limit,
			}
		}
	}

	return nil
}

// FormatAPIError produces a user-friendly progress message while retrying.
// For the message shown when the turn actually ends, use FriendlyAPIError.
func FormatAPIError(err error, attempt, maxRetries int) string {
	cat := ClassifyError(err)
	switch cat {
	case ErrCategoryRateLimit:
		retryAfter := ParseRetryAfter(err)
		if retryAfter > 0 {
			return fmt.Sprintf("Rate limited (attempt %d/%d). Retrying in %v...", attempt, maxRetries, retryAfter.Round(time.Second))
		}
		return fmt.Sprintf("Rate limited (attempt %d/%d). Retrying with backoff...", attempt, maxRetries)
	case ErrCategoryTransient:
		return fmt.Sprintf("Temporary API error (attempt %d/%d). Retrying...", attempt, maxRetries)
	case ErrCategoryContextOverflow:
		info := ParseContextOverflow(err)
		if info != nil {
			return fmt.Sprintf("Context overflow: %d tokens exceeds limit of %d (by %d tokens). Compaction needed.",
				info.ActualTokens, info.LimitTokens, info.TokenGap)
		}
		return "Context overflow: input too long for model. Compaction needed."
	case ErrCategoryAuth:
		return "Authentication error. Please check your API key and provider configuration."
	case ErrCategoryQuota:
		return "Out of quota. Not retrying — waiting will not help."
	default:
		return fmt.Sprintf("API error: %v", err)
	}
}

// FriendlyAPIError renders the message a *user* sees when a turn dies on an API
// error. provider and model may be empty.
//
// Three rules, learned from getting this wrong:
//
//  1. Name the cause in the first clause. "Rate limited by openai" beats
//     "Error: 429 status code (429) …". The raw provider payload is for the log.
//  2. Say what to do. An error the reader cannot act on is just an apology. Rate
//     limit → wait (and say how long if the provider told us). Quota → top up,
//     with the console URL when we know it. Auth → fix the key.
//  3. Never imply the work happened. This function exists because a 402 was
//     being reported as a clean end_turn, so the agent looked like it had
//     finished thinking and simply had nothing to say — 310 eval runs scored as
//     passes on a model that never ran. Silence is the one thing an error must
//     never look like.
func FriendlyAPIError(err error, provider, model string) string {
	if err == nil {
		return ""
	}
	where := ""
	switch {
	case provider != "" && model != "":
		where = fmt.Sprintf(" by %s (%s)", provider, model)
	case provider != "":
		where = " by " + provider
	}

	switch ClassifyError(err) {
	case ErrCategoryRateLimit:
		if d := ParseRetryAfter(err); d > 0 {
			return fmt.Sprintf("Rate limited%s, and retries didn't clear it. The provider asked to wait %v. "+
				"Nothing was lost — send the message again after that, or switch models with /model.",
				where, d.Round(time.Second))
		}
		return fmt.Sprintf("Rate limited%s, and retries didn't clear it. "+
			"Nothing was lost — wait a moment and send the message again, or switch models with /model.", where)

	case ErrCategoryQuota:
		if unpurchasedModel(err) {
			return fmt.Sprintf("Model access is not enabled%s. Open the provider console to purchase or enable access to this model, then retry. Or switch to another configured model with /model.", where)
		}
		msg := fmt.Sprintf("Out of quota%s — the account has no credit left for this model, "+
			"so I stopped without running anything.", where)
		// Prefer a URL the provider itself put in the error over our table: it is
		// current, it is account-specific, and it is right even for a provider we
		// have never heard of (a custom endpoint has no table entry at all, and
		// that is exactly when the user most needs pointing somewhere).
		if url := urlInError(err); url != "" {
			msg += "\nTop up or upgrade: " + url
		} else if url := quotaConsoleURL(provider); url != "" {
			msg += "\nTop up or enable billing: " + url
		}
		return msg + "\nOr switch to another configured model with /model."

	case ErrCategoryAuth:
		return fmt.Sprintf("The API key%s was rejected. Check the key in ~/.jcode/config.json "+
			"(or the provider's env var), then try again.", where)

	case ErrCategoryContextOverflow:
		if info := ParseContextOverflow(err); info != nil {
			return fmt.Sprintf("The conversation is %d tokens, over this model's %d-token limit by %d. "+
				"Run /compact to summarize the history, or switch to a model with a bigger window.",
				info.ActualTokens, info.LimitTokens, info.TokenGap)
		}
		return "The conversation is too long for this model. Run /compact to summarize the history, " +
			"or switch to a model with a bigger window."

	case ErrCategoryTransient:
		return fmt.Sprintf("Could not reach the model%s after several retries: %v\n"+
			"This is usually temporary — try again.", where, cleanErr(err))
	}
	return fmt.Sprintf("The model%s returned an error: %v", where, cleanErr(err))
}

// FriendlyError wraps a raw API error so that anything printing err.Error()
// shows the human message instead of the provider's wire payload.
//
// It exists because there are three frontends (TUI, web, ACP) and one of them
// was printing `[NodeRunError] error, status code: 429, status: 429 Too Many
// Requests, message: ...\nnode path: [node_1, ChatModel]` straight to the user.
// Fixing that per-frontend means fixing it three times and forgetting the
// fourth; wrapping at the single choke point in runner.Run fixes it once.
//
// The raw error stays reachable via Unwrap, so logs and classification keep the
// full payload and only the *display* changes.
type FriendlyError struct {
	Err      error
	Message  string
	Category APIErrorCategory
}

func (e *FriendlyError) Error() string { return e.Message }
func (e *FriendlyError) Unwrap() error { return e.Err }

// Raw returns the underlying provider error, for logs.
func (e *FriendlyError) Raw() error { return e.Err }

// WrapFriendly returns err wrapped with a human-readable message, or err
// unchanged when it is nil, already wrapped, or a plain context cancellation
// (which is not a failure and must keep its identity for errors.Is checks).
func WrapFriendly(err error, provider, model string) error {
	if err == nil {
		return nil
	}
	var already *FriendlyError
	if asFriendly(err, &already) {
		return err
	}
	if strings.Contains(err.Error(), "context canceled") ||
		strings.Contains(err.Error(), "context deadline exceeded") {
		return err
	}
	return &FriendlyError{
		Err:      err,
		Message:  FriendlyAPIError(err, provider, model),
		Category: ClassifyError(err),
	}
}

func asFriendly(err error, target **FriendlyError) bool {
	for err != nil {
		if fe, ok := err.(*FriendlyError); ok {
			*target = fe
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// billingURLRe finds a URL in an error message. Bounded to http(s) and stopped
// at whitespace or a closing bracket so a trailing "." or ")" is not swallowed.
var billingURLRe = regexp.MustCompile(`https?://[^\s<>"'\)\]]+`)

// urlInError extracts a URL the provider put in its own error, which is how
// Moonshot, OpenAI and several others tell you where to pay. It beats our table:
// it is current, account-specific, and present even for a custom endpoint we
// have no table entry for — which is precisely when a user is most stuck.
func urlInError(err error) string {
	if err == nil {
		return ""
	}
	return billingURLRe.FindString(err.Error())
}

// quotaConsoleURL returns the billing page for providers we know, so the user
// does not have to go hunting for it. Empty for unknown providers — a wrong URL
// is worse than none.
func quotaConsoleURL(provider string) string {
	switch {
	case strings.HasPrefix(provider, "tencent-tokenhub"):
		return "https://console.cloud.tencent.com/tokenhub/inference"
	case strings.HasPrefix(provider, "openai"):
		return "https://platform.openai.com/settings/organization/billing"
	case strings.HasPrefix(provider, "anthropic"):
		return "https://console.anthropic.com/settings/billing"
	case strings.HasPrefix(provider, "zhipuai"), strings.HasPrefix(provider, "bigmodel"):
		return "https://bigmodel.cn/usercenter/financialaccount"
	case strings.HasPrefix(provider, "moonshot"):
		return "https://platform.moonshot.cn/console/account"
	case strings.HasPrefix(provider, "deepseek"):
		return "https://platform.deepseek.com/usage"
	case strings.HasPrefix(provider, "alibaba"), strings.HasPrefix(provider, "dashscope"):
		return "https://bailian.console.aliyun.com"
	case strings.HasPrefix(provider, "minimax"):
		return "https://platform.minimaxi.com/user-center/basic-information"
	}
	return ""
}

// cleanErr strips the framework wrapping that makes an error read like a stack
// trace ("[NodeRunError] error, status code: 402, status: 402 Payment Required,
// message: ..."). The user wants the message, not the plumbing.
func cleanErr(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "[NodeRunError] ")
	if i := strings.Index(msg, "message: "); i >= 0 {
		msg = msg[i+len("message: "):]
	}
	if i := strings.Index(msg, "\nnode path:"); i >= 0 {
		msg = msg[:i]
	}
	return strings.TrimSpace(msg)
}
