package bot

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

// persistentOperationKind is deliberately transport-neutral. One operation is
// exactly one persistent transport call; draft updates and typing indicators
// never enter a DeliveryPlan.
type persistentOperationKind string

const (
	persistentOperationRichText   persistentOperationKind = "rich_text"
	persistentOperationLegacyText persistentOperationKind = "legacy_text"
	persistentOperationRichMedia  persistentOperationKind = "rich_media"
	persistentOperationMedia      persistentOperationKind = "media"
)

// persistentSendResult is the complete stable identity returned by one
// persistent operation. A Telegram rich send has one ID; a legacy album may
// have several. Empty, blank, or duplicate IDs make the outcome unknown.
type persistentSendResult struct {
	MessageIDs []string
}

func (r persistentSendResult) primaryMessageID() string {
	if len(r.MessageIDs) == 0 {
		return ""
	}
	return r.MessageIDs[0]
}

// persistentMediaTransport exposes all message IDs created by one logical
// media call. It is optional so transports without multi-message media keep the
// established Transport contract.
type persistentMediaTransport interface {
	SendMediaPersistent(ctx context.Context, m OutgoingMedia) (persistentSendResult, error)
}

// persistentTextTransport bypasses compatibility retries and hard-splitting:
// one call of this method is exactly one persistent transport request. V2
// plans use it so a ledger operation never hides a second non-idempotent send.
type persistentTextTransport interface {
	SendTextPersistent(ctx context.Context, r OutgoingResponse) (string, error)
}

// deliveryOperation is a closed tagged union. Exactly one matching payload
// must be populated. formatFallback is a fully preflighted suffix used only
// after a confirmed rich-format rejection; it is never built after a request.
type deliveryOperation struct {
	Kind persistentOperationKind

	Text      *OutgoingResponse
	RichMedia *OutgoingRichMedia
	Media     *OutgoingMedia

	formatFallback []deliveryOperation
}

// deliveryPlan is immutable after preflight. The executor validates the whole
// primary plan and every fallback suffix before the first network call.
type deliveryPlan struct {
	Operations []deliveryOperation
}

type deliveryLedgerContext struct {
	UserID         storage.ScopeID
	Transport      string
	ConversationID string
}

func (p deliveryPlan) validate() error {
	if len(p.Operations) == 0 {
		return errors.New("delivery plan has no persistent operations")
	}
	if len(p.Operations) > richMessageMaxFallbackChunks {
		return fmt.Errorf("delivery plan has %d operations, limit is %d", len(p.Operations), richMessageMaxFallbackChunks)
	}
	for i := range p.Operations {
		if err := validateDeliveryOperation(p.Operations[i], true); err != nil {
			return fmt.Errorf("delivery operation %d: %w", i, err)
		}
	}
	return nil
}

func validateDeliveryOperation(op deliveryOperation, allowFallback bool) error {
	payloads := 0
	if op.Text != nil {
		payloads++
	}
	if op.RichMedia != nil {
		payloads++
	}
	if op.Media != nil {
		payloads++
	}
	if payloads != 1 {
		return fmt.Errorf("kind %q has %d payloads, want exactly one", op.Kind, payloads)
	}
	switch op.Kind {
	case persistentOperationRichText:
		if op.Text == nil || strings.TrimSpace(op.Text.ConversationID) == "" ||
			op.Text.Format != ResponseFormatRichHTML || strings.TrimSpace(op.Text.Text) == "" {
			return errors.New("rich text operation has an invalid payload")
		}
	case persistentOperationLegacyText:
		if op.Text == nil || strings.TrimSpace(op.Text.ConversationID) == "" ||
			op.Text.Format == ResponseFormatRichHTML || strings.TrimSpace(op.Text.Text) == "" {
			return errors.New("legacy text operation has an invalid payload")
		}
	case persistentOperationRichMedia:
		if op.RichMedia == nil || strings.TrimSpace(op.RichMedia.ConversationID) == "" ||
			strings.TrimSpace(op.RichMedia.HTML) == "" || len(op.RichMedia.Items) == 0 || len(op.RichMedia.Items) > generatedRichGalleryMax {
			return errors.New("rich media operation has an invalid payload")
		}
		for i, item := range op.RichMedia.Items {
			if err := validatePersistentMediaItem(item); err != nil {
				return fmt.Errorf("rich media item %d: %w", i, err)
			}
		}
	case persistentOperationMedia:
		if op.Media == nil || strings.TrimSpace(op.Media.ConversationID) == "" || len(op.Media.Items) == 0 || len(op.Media.Items) > 10 {
			return errors.New("media operation has an invalid payload")
		}
		for i, item := range op.Media.Items {
			if err := validatePersistentMediaItem(item); err != nil {
				return fmt.Errorf("media item %d: %w", i, err)
			}
		}
	default:
		return fmt.Errorf("unsupported operation kind %q", op.Kind)
	}
	if len(op.formatFallback) > 0 && op.Kind != persistentOperationRichText && op.Kind != persistentOperationRichMedia {
		return errors.New("format fallback is allowed only on rich operations")
	}
	if !allowFallback && len(op.formatFallback) != 0 {
		return errors.New("nested format fallback is not allowed")
	}
	if len(op.formatFallback) > richMessageMaxFallbackChunks {
		return fmt.Errorf("format fallback has %d operations, limit is %d", len(op.formatFallback), richMessageMaxFallbackChunks)
	}
	for i := range op.formatFallback {
		if op.formatFallback[i].Kind != persistentOperationLegacyText && op.formatFallback[i].Kind != persistentOperationMedia {
			return fmt.Errorf("format fallback operation %d is not a legacy leaf", i)
		}
		if err := validateDeliveryOperation(op.formatFallback[i], false); err != nil {
			return fmt.Errorf("format fallback operation %d: %w", i, err)
		}
	}
	return nil
}

func validatePersistentMediaItem(item OutgoingMediaItem) error {
	if len(item.Data) == 0 {
		return errors.New("data is empty")
	}
	if strings.TrimSpace(item.Filename) == "" || strings.TrimSpace(item.MIME) == "" {
		return errors.New("filename and MIME are required")
	}
	if strings.ContainsAny(item.Filename, "\r\n\x00") || strings.ContainsAny(item.MIME, "\r\n\x00") {
		return errors.New("filename or MIME contains control characters")
	}
	if _, _, err := mime.ParseMediaType(strings.TrimSpace(item.MIME)); err != nil {
		return fmt.Errorf("invalid MIME type: %w", err)
	}
	return nil
}

func persistentMediaType(item OutgoingMediaItem) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(item.MIME))
	if err != nil {
		return ""
	}
	return strings.ToLower(mediaType)
}

func outcomeAfterFailure(confirmed int, err error) richDeliveryOutcome {
	base := richOutcomeForError(err)
	if confirmed == 0 {
		return base
	}
	if base == richDeliveryRejected {
		return richDeliveryPartialRejected
	}
	return richDeliveryPartialUnknown
}

func normalizePersistentIDs(ids []string, seen map[string]struct{}) ([]string, error) {
	if len(ids) == 0 {
		return nil, errors.New("persistent operation returned no message ids")
	}
	out := make([]string, 0, len(ids))
	local := make(map[string]struct{}, len(ids))
	for i, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("persistent operation returned a blank message id at index %d", i)
		}
		if _, ok := local[id]; ok {
			return nil, fmt.Errorf("persistent operation returned duplicate message id %q", id)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("delivery returned message id %q more than once", id)
		}
		local[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// normalizeOptionalPersistentIDs is used on error returns. Compatibility
// transports commonly return one empty string alongside an error; that is not
// a confirmation. Conversely, a complete set of stable IDs is stronger than a
// contradictory local error for a one-call operation and must not be thrown
// away (or resent).
func normalizeOptionalPersistentIDs(ids []string, seen map[string]struct{}) ([]string, error) {
	hasStable := false
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			hasStable = true
			break
		}
	}
	if !hasStable {
		return nil, nil
	}
	return normalizePersistentIDs(ids, seen)
}

func withoutReply(op deliveryOperation) deliveryOperation {
	switch {
	case op.Text != nil:
		copy := *op.Text
		copy.ReplyTo = ""
		op.Text = &copy
	case op.RichMedia != nil:
		copy := *op.RichMedia
		copy.ReplyTo = ""
		op.RichMedia = &copy
	case op.Media != nil:
		copy := *op.Media
		copy.ReplyTo = ""
		op.Media = &copy
	}
	return op
}

func (b *Bot) executePersistentOperation(ctx context.Context, op deliveryOperation) (persistentSendResult, error) {
	switch op.Kind {
	case persistentOperationRichText, persistentOperationLegacyText:
		transport, ok := b.transport.(persistentTextTransport)
		if !ok {
			id, err := b.transport.SendText(ctx, *op.Text)
			return persistentSendResult{MessageIDs: []string{id}}, err
		}
		id, err := transport.SendTextPersistent(ctx, *op.Text)
		return persistentSendResult{MessageIDs: []string{id}}, err
	case persistentOperationRichMedia:
		transport, ok := b.transport.(RichMediaTransport)
		if !ok {
			return persistentSendResult{}, errors.New("transport does not support rich media")
		}
		id, err := transport.SendRichMedia(ctx, *op.RichMedia)
		return persistentSendResult{MessageIDs: []string{id}}, err
	case persistentOperationMedia:
		transport, ok := b.transport.(persistentMediaTransport)
		if !ok {
			return persistentSendResult{}, errors.New("transport does not support exact persistent media operations")
		}
		return transport.SendMediaPersistent(ctx, *op.Media)
	default:
		return persistentSendResult{}, fmt.Errorf("unsupported operation kind %q", op.Kind)
	}
}

func deliveryOperationConversationID(op deliveryOperation) string {
	switch {
	case op.Text != nil:
		return op.Text.ConversationID
	case op.RichMedia != nil:
		return op.RichMedia.ConversationID
	case op.Media != nil:
		return op.Media.ConversationID
	default:
		return ""
	}
}

func validateTelegramRoutingValue(name, value string, allowEmpty bool) error {
	if strings.TrimSpace(value) == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%s is empty", name)
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("%s is not a positive Telegram id", name)
	}
	return nil
}

func validateTelegramDeliveryOperation(op deliveryOperation, threshold int) error {
	if err := validateTelegramRoutingValue("conversation id", deliveryOperationConversationID(op), false); err != nil {
		return err
	}
	var threadRoot, replyTo string
	switch {
	case op.Text != nil:
		threadRoot, replyTo = op.Text.ThreadRoot, op.Text.ReplyTo
	case op.RichMedia != nil:
		threadRoot, replyTo = op.RichMedia.ThreadRoot, op.RichMedia.ReplyTo
	case op.Media != nil:
		threadRoot, replyTo = op.Media.ThreadRoot, op.Media.ReplyTo
	}
	if err := validateTelegramRoutingValue("thread root", threadRoot, true); err != nil {
		return err
	}
	if err := validateTelegramRoutingValue("reply id", replyTo, true); err != nil {
		return err
	}
	if op.RichMedia != nil {
		for i, item := range op.RichMedia.Items {
			if item.AsDocument || !strings.HasPrefix(persistentMediaType(item), "image/") || !generatedPhotoCanBePreviewed(item) {
				return fmt.Errorf("rich media item %d is not a valid Telegram photo", i)
			}
		}
	}
	if op.Media != nil {
		firstDocument := op.Media.Items[0].AsDocument || (threshold > 0 && len(op.Media.Items[0].Data) > threshold)
		for i, item := range op.Media.Items {
			asDocument := item.AsDocument || (threshold > 0 && len(item.Data) > threshold)
			if asDocument != firstDocument {
				return fmt.Errorf("media operation mixes photo and document at index %d", i)
			}
			if asDocument {
				if len(item.Data) > telegramDocumentMaxBytes {
					return fmt.Errorf("document item %d has %d bytes, limit is %d", i, len(item.Data), telegramDocumentMaxBytes)
				}
			} else if !strings.HasPrefix(persistentMediaType(item), "image/") || !generatedPhotoCanBePreviewed(item) {
				return fmt.Errorf("photo item %d is outside the Telegram photo envelope", i)
			}
		}
	}
	return nil
}

// validateDeliveryExecution mirrors every transport-local invariant before a
// ledger operation can enter sending. The executor may then treat any error
// from the transport method as an ambiguous result of one actual API call.
func (b *Bot) validateDeliveryExecution(plan deliveryPlan, metadata []deliveryLedgerContext) error {
	if len(metadata) > 1 {
		return errors.New("delivery executor received more than one ledger context")
	}
	exactRequired := len(metadata) == 1
	var validateOperation func(deliveryOperation) error
	validateOperation = func(op deliveryOperation) error {
		if exactRequired && deliveryOperationConversationID(op) != metadata[0].ConversationID {
			return errors.New("operation conversation does not match ledger context")
		}
		if exactRequired && (op.Kind == persistentOperationRichText || op.Kind == persistentOperationLegacyText) {
			if _, ok := b.transport.(persistentTextTransport); !ok {
				return errors.New("transport does not support exact persistent text operations")
			}
		}
		if op.Kind == persistentOperationRichMedia {
			if _, ok := b.transport.(RichMediaTransport); !ok {
				return errors.New("transport does not support rich media")
			}
		}
		if op.Kind == persistentOperationMedia {
			if _, ok := b.transport.(persistentMediaTransport); !ok {
				return errors.New("transport does not support exact persistent media operations")
			}
		}
		if b.transport.Kind() == transportTelegram {
			if err := validateTelegramDeliveryOperation(op, b.cfg.Agents.ImageGenerator.DocumentThresholdBytes); err != nil {
				return err
			}
		}
		for i := range op.formatFallback {
			if err := validateOperation(op.formatFallback[i]); err != nil {
				return fmt.Errorf("format fallback operation %d: %w", i, err)
			}
		}
		return nil
	}
	for i, op := range plan.Operations {
		if err := validateOperation(op); err != nil {
			return fmt.Errorf("delivery operation %d: %w", i, err)
		}
	}
	if exactRequired {
		meta := metadata[0]
		if meta.UserID == "" || strings.TrimSpace(meta.Transport) == "" || strings.TrimSpace(meta.ConversationID) == "" {
			return errors.New("delivery ledger metadata is incomplete")
		}
		if meta.Transport != b.transport.Kind() {
			return errors.New("delivery ledger transport does not match active transport")
		}
	}
	return nil
}

// executeDeliveryPlan performs no implicit retry. After an unknown result it
// stops immediately; after a confirmed rich-format rejection it may execute
// only the immutable fallback suffix prepared before the first request.
func storageOperationKind(kind persistentOperationKind) (storage.DeliveryOperationKind, error) {
	switch kind {
	case persistentOperationRichText:
		return storage.DeliveryOperationRichText, nil
	case persistentOperationLegacyText:
		return storage.DeliveryOperationLegacyText, nil
	case persistentOperationRichMedia:
		return storage.DeliveryOperationRichMedia, nil
	case persistentOperationMedia:
		return storage.DeliveryOperationMedia, nil
	default:
		return "", fmt.Errorf("unsupported operation kind %q", kind)
	}
}

func storageOperations(operations []deliveryOperation) ([]storage.OutboundDeliveryOperation, error) {
	out := make([]storage.OutboundDeliveryOperation, len(operations))
	for i, op := range operations {
		kind, err := storageOperationKind(op.Kind)
		if err != nil {
			return nil, err
		}
		out[i] = storage.OutboundDeliveryOperation{Ordinal: i, Kind: kind}
	}
	return out, nil
}

func deliveryErrorClass(err error) storage.DeliveryErrorClass {
	if errors.Is(err, ErrRichMessageRejected) {
		return storage.DeliveryErrorFormat
	}
	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == 429:
			return storage.DeliveryErrorRateLimit
		case apiErr.Code >= 500:
			return storage.DeliveryErrorServer
		case apiErr.Code >= 400:
			return storage.DeliveryErrorInternal
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return storage.DeliveryErrorNetwork
	}
	return storage.DeliveryErrorNetwork
}

func traceIDFromContext(ctx context.Context) *string {
	spanContext := trace.SpanFromContext(ctx).SpanContext()
	if !spanContext.HasTraceID() {
		return nil
	}
	value := spanContext.TraceID().String()
	return &value
}

func (b *Bot) createDeliveryLedger(ctx context.Context, plan deliveryPlan, metadata []deliveryLedgerContext) (storage.DeliveryRepository, int64, error) {
	if len(metadata) == 0 {
		return nil, 0, nil
	}
	if b.deliveryRepo == nil {
		return nil, 0, errors.New("delivery ledger context was provided but no delivery repository is configured")
	}
	repo := b.deliveryRepo
	meta := metadata[0]
	if meta.UserID == "" || strings.TrimSpace(meta.Transport) == "" || strings.TrimSpace(meta.ConversationID) == "" {
		return nil, 0, errors.New("delivery ledger metadata is incomplete")
	}
	operations, err := storageOperations(plan.Operations)
	if err != nil {
		return nil, 0, err
	}
	id, err := repo.CreateOutboundDelivery(storage.OutboundDelivery{
		UserID:         meta.UserID,
		Transport:      meta.Transport,
		ConversationID: meta.ConversationID,
		TraceID:        traceIDFromContext(ctx),
	}, operations)
	if err != nil {
		return nil, 0, fmt.Errorf("create outbound delivery ledger: %w", err)
	}
	return repo, id, nil
}

func deliveryOutcomeForLocalStop(confirmed int) richDeliveryOutcome {
	if confirmed > 0 {
		return richDeliveryPartialRejected
	}
	return richDeliveryRejected
}

func (b *Bot) executeDeliveryPlan(ctx context.Context, plan deliveryPlan, metadata ...deliveryLedgerContext) richDeliveryResult {
	if err := plan.validate(); err != nil {
		return richDeliveryResult{outcome: richDeliveryRejected, err: err}
	}
	if err := b.validateDeliveryExecution(plan, metadata); err != nil {
		return richDeliveryResult{outcome: richDeliveryRejected, err: err}
	}

	result := richDeliveryResult{
		metricPath:     richMetricPathNative,
		fallbackReason: richMetricFallbackNone,
	}
	ledger, deliveryID, ledgerErr := b.createDeliveryLedger(ctx, plan, metadata)
	if ledgerErr != nil {
		result.outcome = richDeliveryRejected
		result.err = ledgerErr
		return result
	}
	result.deliveryID = deliveryID
	seenIDs := make(map[string]struct{})
	execute := func(operations []deliveryOperation, allowFormatFallback bool) (stop bool) {
		for i := range operations {
			op := operations[i]
			if len(result.confirmedIDs) > 0 {
				op = withoutReply(op)
			}
			if ledger != nil {
				if err := ledger.MarkOutboundDeliveryOperationSending(deliveryID, i); err != nil {
					result.outcome = deliveryOutcomeForLocalStop(len(result.confirmedIDs))
					result.failedPart = deliveryIndex(i)
					result.err = fmt.Errorf("mark delivery operation %d sending: %w", i, err)
					return true
				}
			}
			result.attempts++
			sent, err := b.executePersistentOperation(ctx, op)
			if err != nil {
				ids, idErr := normalizeOptionalPersistentIDs(sent.MessageIDs, seenIDs)
				if idErr != nil {
					result.outcome = outcomeAfterFailure(len(result.confirmedIDs), idErr)
					result.failedPart = deliveryIndex(i)
					result.err = fmt.Errorf("execute %s operation %d returned invalid ids with an error: %w", op.Kind, i, idErr)
					if ledger != nil {
						if completeErr := ledger.CompleteOutboundDeliveryOperation(deliveryID, i,
							storage.DeliveryOperationStatusUnknown, storage.DeliveryErrorInvalidResponse, nil); completeErr != nil {
							result.err = errors.Join(result.err, fmt.Errorf("record invalid delivery response: %w", completeErr))
						}
					}
					return true
				}
				if len(ids) > 0 {
					b.logger.Warn("persistent transport returned stable ids with an error; treating the one-call operation as confirmed",
						"operation_kind", op.Kind, "message_count", len(ids))
					for _, id := range ids {
						seenIDs[id] = struct{}{}
						result.confirmMessage(id)
					}
					if ledger != nil {
						if completeErr := ledger.CompleteOutboundDeliveryOperation(deliveryID, i,
							storage.DeliveryOperationStatusConfirmed, storage.DeliveryErrorNone, ids); completeErr != nil {
							result.outcome = richDeliveryPartialUnknown
							result.err = errors.Join(err, fmt.Errorf("record confirmed delivery operation: %w", completeErr))
							return true
						}
					}
					continue
				}
				if allowFormatFallback && errors.Is(err, ErrRichMessageRejected) && len(op.formatFallback) > 0 {
					result.metricPath = richMetricPathAPIFallback
					result.fallbackReason = richMetricFallbackFormatRejected
					result.failedPart = deliveryIndex(i)
					var fallbackOrdinals []int
					if ledger != nil {
						if completeErr := ledger.CompleteOutboundDeliveryOperation(deliveryID, i,
							storage.DeliveryOperationStatusRejected, storage.DeliveryErrorFormat, nil); completeErr != nil {
							result.outcome = deliveryOutcomeForLocalStop(len(result.confirmedIDs))
							result.err = errors.Join(err, fmt.Errorf("record format rejection: %w", completeErr))
							return true
						}
						fallbackOps, conversionErr := storageOperations(op.formatFallback)
						if conversionErr != nil {
							result.outcome = deliveryOutcomeForLocalStop(len(result.confirmedIDs))
							result.err = conversionErr
							return true
						}
						fallbackOrdinals, ledgerErr = ledger.ActivateOutboundDeliveryFallback(deliveryID, i, fallbackOps)
						if ledgerErr != nil {
							result.outcome = deliveryOutcomeForLocalStop(len(result.confirmedIDs))
							result.err = errors.Join(err, fmt.Errorf("activate delivery fallback: %w", ledgerErr))
							return true
						}
					}
					return executeFallback(b, ctx, op.formatFallback, i, &result, seenIDs, ledger, deliveryID, fallbackOrdinals)
				}
				result.outcome = outcomeAfterFailure(len(result.confirmedIDs), err)
				result.failedPart = deliveryIndex(i)
				result.err = fmt.Errorf("execute %s operation %d: %w", op.Kind, i, err)
				if ledger != nil {
					status := storage.DeliveryOperationStatusUnknown
					if richOutcomeForError(err) == richDeliveryRejected {
						status = storage.DeliveryOperationStatusRejected
					}
					if completeErr := ledger.CompleteOutboundDeliveryOperation(deliveryID, i, status, deliveryErrorClass(err), nil); completeErr != nil {
						result.err = errors.Join(result.err, fmt.Errorf("complete failed delivery operation: %w", completeErr))
					}
				}
				return true
			}
			ids, idErr := normalizePersistentIDs(sent.MessageIDs, seenIDs)
			if idErr != nil {
				result.outcome = richDeliveryUnknown
				if len(result.confirmedIDs) > 0 {
					result.outcome = richDeliveryPartialUnknown
				}
				result.err = fmt.Errorf("execute %s operation %d: %w", op.Kind, i, idErr)
				result.failedPart = deliveryIndex(i)
				if ledger != nil {
					if completeErr := ledger.CompleteOutboundDeliveryOperation(deliveryID, i,
						storage.DeliveryOperationStatusUnknown, storage.DeliveryErrorInvalidResponse, nil); completeErr != nil {
						result.err = errors.Join(result.err, fmt.Errorf("record invalid delivery response: %w", completeErr))
					}
				}
				return true
			}
			for _, id := range ids {
				seenIDs[id] = struct{}{}
				result.confirmMessage(id)
			}
			if ledger != nil {
				if completeErr := ledger.CompleteOutboundDeliveryOperation(deliveryID, i,
					storage.DeliveryOperationStatusConfirmed, storage.DeliveryErrorNone, ids); completeErr != nil {
					result.outcome = richDeliveryPartialUnknown
					if len(result.confirmedIDs) == len(ids) {
						result.outcome = richDeliveryUnknown
					}
					result.err = fmt.Errorf("record confirmed delivery operation: %w", completeErr)
					return true
				}
			}
		}
		return false
	}
	if execute(plan.Operations, true) {
		return result
	}
	result.outcome = richDeliveryConfirmed
	return result
}

// executeFallback is split out to keep the primary loop non-recursive. Fallback
// operations are validated as leaf operations and therefore cannot trigger a
// second representation change.
func executeFallback(
	b *Bot,
	ctx context.Context,
	operations []deliveryOperation,
	sourcePart int,
	result *richDeliveryResult,
	seenIDs map[string]struct{},
	ledger storage.DeliveryRepository,
	deliveryID int64,
	ledgerOrdinals []int,
) bool {
	for i := range operations {
		op := operations[i]
		if len(result.confirmedIDs) > 0 {
			op = withoutReply(op)
		}
		ledgerOrdinal := i
		if ledger != nil {
			if len(ledgerOrdinals) != len(operations) {
				result.outcome = deliveryOutcomeForLocalStop(len(result.confirmedIDs))
				result.failedPart = deliveryIndex(sourcePart)
				result.failedChunk = deliveryIndex(i)
				result.err = errors.New("delivery fallback ledger ordinals do not match operations")
				return true
			}
			ledgerOrdinal = ledgerOrdinals[i]
			if err := ledger.MarkOutboundDeliveryOperationSending(deliveryID, ledgerOrdinal); err != nil {
				result.outcome = deliveryOutcomeForLocalStop(len(result.confirmedIDs))
				result.failedPart = deliveryIndex(sourcePart)
				result.failedChunk = deliveryIndex(i)
				result.err = fmt.Errorf("mark fallback operation %d sending: %w", i, err)
				return true
			}
		}
		result.attempts++
		sent, err := b.executePersistentOperation(ctx, op)
		if err != nil {
			ids, idErr := normalizeOptionalPersistentIDs(sent.MessageIDs, seenIDs)
			if idErr != nil {
				result.outcome = outcomeAfterFailure(len(result.confirmedIDs), idErr)
				result.failedPart = deliveryIndex(sourcePart)
				result.failedChunk = deliveryIndex(i)
				result.err = fmt.Errorf("execute fallback %s operation %d returned invalid ids with an error: %w", op.Kind, i, idErr)
				if ledger != nil {
					if completeErr := ledger.CompleteOutboundDeliveryOperation(deliveryID, ledgerOrdinal,
						storage.DeliveryOperationStatusUnknown, storage.DeliveryErrorInvalidResponse, nil); completeErr != nil {
						result.err = errors.Join(result.err, fmt.Errorf("record invalid fallback response: %w", completeErr))
					}
				}
				return true
			}
			if len(ids) > 0 {
				b.logger.Warn("persistent transport returned stable ids with an error; treating the fallback operation as confirmed",
					"operation_kind", op.Kind, "message_count", len(ids))
				for _, id := range ids {
					seenIDs[id] = struct{}{}
					result.confirmMessage(id)
				}
				if ledger != nil {
					if completeErr := ledger.CompleteOutboundDeliveryOperation(deliveryID, ledgerOrdinal,
						storage.DeliveryOperationStatusConfirmed, storage.DeliveryErrorNone, ids); completeErr != nil {
						result.outcome = richDeliveryPartialUnknown
						result.err = errors.Join(err, fmt.Errorf("record confirmed fallback operation: %w", completeErr))
						return true
					}
				}
				continue
			}
			result.outcome = outcomeAfterFailure(len(result.confirmedIDs), err)
			result.failedPart = deliveryIndex(sourcePart)
			result.failedChunk = deliveryIndex(i)
			result.err = fmt.Errorf("execute fallback %s operation %d: %w", op.Kind, i, err)
			if ledger != nil {
				status := storage.DeliveryOperationStatusUnknown
				if richOutcomeForError(err) == richDeliveryRejected {
					status = storage.DeliveryOperationStatusRejected
				}
				if completeErr := ledger.CompleteOutboundDeliveryOperation(deliveryID, ledgerOrdinal,
					status, deliveryErrorClass(err), nil); completeErr != nil {
					result.err = errors.Join(result.err, fmt.Errorf("complete fallback operation: %w", completeErr))
				}
			}
			return true
		}
		ids, idErr := normalizePersistentIDs(sent.MessageIDs, seenIDs)
		if idErr != nil {
			result.outcome = richDeliveryUnknown
			if len(result.confirmedIDs) > 0 {
				result.outcome = richDeliveryPartialUnknown
			}
			result.err = fmt.Errorf("execute fallback %s operation %d: %w", op.Kind, i, idErr)
			result.failedPart = deliveryIndex(sourcePart)
			result.failedChunk = deliveryIndex(i)
			if ledger != nil {
				if completeErr := ledger.CompleteOutboundDeliveryOperation(deliveryID, ledgerOrdinal,
					storage.DeliveryOperationStatusUnknown, storage.DeliveryErrorInvalidResponse, nil); completeErr != nil {
					result.err = errors.Join(result.err, fmt.Errorf("record invalid fallback response: %w", completeErr))
				}
			}
			return true
		}
		for _, id := range ids {
			seenIDs[id] = struct{}{}
			result.confirmMessage(id)
		}
		if ledger != nil {
			if completeErr := ledger.CompleteOutboundDeliveryOperation(deliveryID, ledgerOrdinal,
				storage.DeliveryOperationStatusConfirmed, storage.DeliveryErrorNone, ids); completeErr != nil {
				result.outcome = richDeliveryPartialUnknown
				result.err = fmt.Errorf("record confirmed fallback operation: %w", completeErr)
				return true
			}
		}
	}
	result.outcome = richDeliveryConfirmed
	return true
}
