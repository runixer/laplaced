package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/artifactdelivery"
)

var (
	_ ExactMessageRepository      = (*Store)(nil)
	_ DeliveryRepository          = (*Store)(nil)
	_ ArtifactReferenceRepository = (*Store)(nil)
)

// TransportMessage is one persistent transport message belonging to a logical
// assistant reply. ConversationID is mandatory because native message ids are
// not necessarily globally unique (notably outside Telegram private chats).
type TransportMessage struct {
	Transport      string
	ConversationID string
	MessageID      string
	Ordinal        int
	IsPrimary      bool
}

type DeliveryStatus string

const (
	DeliveryStatusPlanned         DeliveryStatus = "planned"
	DeliveryStatusSending         DeliveryStatus = "sending"
	DeliveryStatusConfirmed       DeliveryStatus = "confirmed"
	DeliveryStatusRejected        DeliveryStatus = "rejected"
	DeliveryStatusPartialRejected DeliveryStatus = "partial_rejected"
	DeliveryStatusUnknown         DeliveryStatus = "unknown"
	DeliveryStatusPartialUnknown  DeliveryStatus = "partial_unknown"
)

type DeliveryOperationStatus string

const (
	DeliveryOperationStatusPlanned        DeliveryOperationStatus = "planned"
	DeliveryOperationStatusSending        DeliveryOperationStatus = "sending"
	DeliveryOperationStatusConfirmed      DeliveryOperationStatus = "confirmed"
	DeliveryOperationStatusRejected       DeliveryOperationStatus = "rejected"
	DeliveryOperationStatusFormatRejected DeliveryOperationStatus = "format_rejected"
	DeliveryOperationStatusSkipped        DeliveryOperationStatus = "skipped"
	DeliveryOperationStatusUnknown        DeliveryOperationStatus = "unknown"
)

// DeliveryOperationKind mirrors the closed persistent-operation union in the
// delivery planner. Keeping this bounded prevents arbitrary caller strings
// from turning the content-free ledger into an accidental logging channel.
type DeliveryOperationKind string

const (
	DeliveryOperationRichText   DeliveryOperationKind = "rich_text"
	DeliveryOperationLegacyText DeliveryOperationKind = "legacy_text"
	DeliveryOperationRichMedia  DeliveryOperationKind = "rich_media"
	DeliveryOperationMedia      DeliveryOperationKind = "media"
)

// DeliveryErrorClass is intentionally bounded. The ledger must never receive a
// raw error string because it may contain a URL, response fragment, or content.
type DeliveryErrorClass string

const (
	DeliveryErrorNone            DeliveryErrorClass = ""
	DeliveryErrorFormat          DeliveryErrorClass = "format"
	DeliveryErrorRateLimit       DeliveryErrorClass = "rate_limit"
	DeliveryErrorNetwork         DeliveryErrorClass = "network"
	DeliveryErrorServer          DeliveryErrorClass = "server"
	DeliveryErrorInvalidResponse DeliveryErrorClass = "invalid_response"
	DeliveryErrorInternal        DeliveryErrorClass = "internal"
	DeliveryErrorInterrupted     DeliveryErrorClass = "interrupted"
)

type OutboundDelivery struct {
	ID             int64
	UserID         ScopeID
	Transport      string
	ConversationID string
	TraceID        *string
	HistoryID      *int64
	Status         DeliveryStatus
	OperationCount int
	ConfirmedCount int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type OutboundDeliveryOperation struct {
	ID                  int64
	DeliveryID          int64
	Ordinal             int
	Kind                DeliveryOperationKind
	Status              DeliveryOperationStatus
	ErrorClass          DeliveryErrorClass
	StartedAt           *time.Time
	FinishedAt          *time.Time
	CreatedAt           time.Time
	TransportMessageIDs []string
}

func (s *Store) CreateOutboundDelivery(delivery OutboundDelivery, operations []OutboundDeliveryOperation) (int64, error) {
	if delivery.UserID == "" || strings.TrimSpace(delivery.Transport) == "" || strings.TrimSpace(delivery.ConversationID) == "" {
		return 0, errors.New("create outbound delivery: user, transport, and conversation are required")
	}
	if len(operations) == 0 {
		return 0, errors.New("create outbound delivery: at least one operation is required")
	}
	seen := make(map[int]struct{}, len(operations))
	for _, op := range operations {
		if op.Ordinal < 0 || !validDeliveryOperationKind(op.Kind) {
			return 0, fmt.Errorf("create outbound delivery: invalid operation ordinal=%d kind=%q", op.Ordinal, op.Kind)
		}
		if _, ok := seen[op.Ordinal]; ok {
			return 0, fmt.Errorf("create outbound delivery: duplicate operation ordinal %d", op.Ordinal)
		}
		seen[op.Ordinal] = struct{}{}
	}
	for ordinal := range operations {
		if _, ok := seen[ordinal]; !ok {
			return 0, fmt.Errorf("create outbound delivery: operation ordinals must be contiguous from zero (missing %d)", ordinal)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("create outbound delivery: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	deliveryID, err := s.insertReturningIDTx(tx, `INSERT INTO outbound_deliveries
		(user_id, transport, conversation_id, trace_id, status, operation_count, confirmed_count)
		VALUES (?, ?, ?, ?, ?, ?, 0)`, "id", delivery.UserID, delivery.Transport,
		delivery.ConversationID, delivery.TraceID, DeliveryStatusPlanned, len(operations))
	if err != nil {
		return 0, fmt.Errorf("create outbound delivery: insert delivery: %w", err)
	}
	for _, op := range operations {
		if _, err := tx.Exec(s.rebind(`INSERT INTO outbound_delivery_ops
			(delivery_id, ordinal, kind, status) VALUES (?, ?, ?, ?)`),
			deliveryID, op.Ordinal, op.Kind, DeliveryOperationStatusPlanned); err != nil {
			return 0, fmt.Errorf("create outbound delivery: insert operation %d: %w", op.Ordinal, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("create outbound delivery: commit: %w", err)
	}
	return deliveryID, nil
}

func (s *Store) MarkOutboundDeliveryOperationSending(deliveryID int64, ordinal int) error {
	if deliveryID <= 0 || ordinal < 0 {
		return errors.New("mark outbound operation sending: invalid delivery id or ordinal")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("mark outbound operation sending: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var deliveryStatus DeliveryStatus
	if err := tx.QueryRow(s.rebind(`SELECT status FROM outbound_deliveries WHERE id = ?`), deliveryID).Scan(&deliveryStatus); err != nil {
		return fmt.Errorf("mark outbound operation sending: delivery lookup: %w", err)
	}
	if deliveryStatus != DeliveryStatusPlanned && deliveryStatus != DeliveryStatusSending {
		return fmt.Errorf("mark outbound operation sending: delivery is terminal (%s)", deliveryStatus)
	}
	result, err := tx.Exec(s.rebind(`UPDATE outbound_delivery_ops
		SET status = ?, started_at = CURRENT_TIMESTAMP
		WHERE delivery_id = ? AND ordinal = ? AND status = ?`),
		DeliveryOperationStatusSending, deliveryID, ordinal, DeliveryOperationStatusPlanned)
	if err != nil {
		return fmt.Errorf("mark outbound operation sending: update operation: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark outbound operation sending: rows affected: %w", err)
	}
	if updated != 1 {
		var current DeliveryOperationStatus
		lookupErr := tx.QueryRow(s.rebind(`SELECT status FROM outbound_delivery_ops
			WHERE delivery_id = ? AND ordinal = ?`), deliveryID, ordinal).Scan(&current)
		if lookupErr != nil {
			return fmt.Errorf("mark outbound operation sending: lookup after failed transition: %w", lookupErr)
		}
		return fmt.Errorf("mark outbound operation sending: operation is %s, want planned", current)
	}
	if _, err := tx.Exec(s.rebind(`UPDATE outbound_deliveries SET status = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status IN (?, ?)`), DeliveryStatusSending, deliveryID,
		DeliveryStatusPlanned, DeliveryStatusSending); err != nil {
		return fmt.Errorf("mark outbound operation sending: update delivery: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mark outbound operation sending: commit: %w", err)
	}
	return nil
}

func (s *Store) CompleteOutboundDeliveryOperation(deliveryID int64, ordinal int, status DeliveryOperationStatus, errorClass DeliveryErrorClass, transportMessageIDs []string) error {
	if deliveryID <= 0 || ordinal < 0 {
		return errors.New("complete outbound operation: invalid delivery id or ordinal")
	}
	if status != DeliveryOperationStatusConfirmed && status != DeliveryOperationStatusRejected && status != DeliveryOperationStatusUnknown {
		return fmt.Errorf("complete outbound operation: invalid terminal status %q", status)
	}
	if !validDeliveryErrorClass(errorClass) {
		return fmt.Errorf("complete outbound operation: invalid error class %q", errorClass)
	}
	if status == DeliveryOperationStatusConfirmed {
		if errorClass != DeliveryErrorNone || len(transportMessageIDs) == 0 {
			return errors.New("complete outbound operation: confirmed requires message ids and no error class")
		}
	} else if errorClass == DeliveryErrorNone || len(transportMessageIDs) != 0 {
		return errors.New("complete outbound operation: failed/unknown requires an error class and no message ids")
	}
	seenIDs := make(map[string]struct{}, len(transportMessageIDs))
	for _, messageID := range transportMessageIDs {
		if strings.TrimSpace(messageID) == "" {
			return errors.New("complete outbound operation: empty transport message id")
		}
		if _, exists := seenIDs[messageID]; exists {
			return fmt.Errorf("complete outbound operation: duplicate transport message id %q", messageID)
		}
		seenIDs[messageID] = struct{}{}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("complete outbound operation: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var opID int64
	var current DeliveryOperationStatus
	err = tx.QueryRow(s.rebind(`SELECT id, status FROM outbound_delivery_ops
		WHERE delivery_id = ? AND ordinal = ?`), deliveryID, ordinal).Scan(&opID, &current)
	if err != nil {
		return fmt.Errorf("complete outbound operation: lookup: %w", err)
	}
	if current != DeliveryOperationStatusSending {
		return fmt.Errorf("complete outbound operation: operation is %s, want sending", current)
	}

	result, err := tx.Exec(s.rebind(`UPDATE outbound_delivery_ops
		SET status = ?, error_class = ?, finished_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = ?`), status, nullableString(string(errorClass)), opID,
		DeliveryOperationStatusSending)
	if err != nil {
		return fmt.Errorf("complete outbound operation: update operation: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("complete outbound operation: rows affected: %w", err)
	}
	if updated != 1 {
		return errors.New("complete outbound operation: operation transition lost to another writer")
	}
	// A failed or unknown persistent call terminates the active primary branch.
	// Seal its unsent suffix in the same transaction so a terminal delivery can
	// never retain planned operations that startup recovery (which deliberately
	// scans only nonterminal deliveries) would leave behind. A subsequent
	// confirmed rich-format fallback remains safe: ActivateOutboundDeliveryFallback
	// accepts the already-skipped primary suffix and appends fresh planned ops.
	if status != DeliveryOperationStatusConfirmed {
		if _, err := tx.Exec(s.rebind(`UPDATE outbound_delivery_ops
			SET status = ?, finished_at = CURRENT_TIMESTAMP
			WHERE delivery_id = ? AND ordinal > ? AND status = ?`),
			DeliveryOperationStatusSkipped, deliveryID, ordinal, DeliveryOperationStatusPlanned); err != nil {
			return fmt.Errorf("complete outbound operation: skip unsent suffix: %w", err)
		}
	}
	for messageOrdinal, messageID := range transportMessageIDs {
		if _, err := tx.Exec(s.rebind(`INSERT INTO outbound_delivery_messages
			(delivery_id, op_id, message_id, ordinal) VALUES (?, ?, ?, ?)`),
			deliveryID, opID, messageID, messageOrdinal); err != nil {
			return fmt.Errorf("complete outbound operation: insert message %d: %w", messageOrdinal, err)
		}
	}

	var confirmedCount, activeCount, hardFailureCount int
	if err := tx.QueryRow(s.rebind(`SELECT
		(SELECT COUNT(*) FROM outbound_delivery_ops WHERE delivery_id = ? AND status = ?),
		(SELECT COUNT(*) FROM outbound_delivery_ops WHERE delivery_id = ? AND status IN (?, ?)),
		(SELECT COUNT(*) FROM outbound_delivery_ops WHERE delivery_id = ? AND status IN (?, ?))
		FROM outbound_deliveries WHERE id = ?`),
		deliveryID, DeliveryOperationStatusConfirmed,
		deliveryID, DeliveryOperationStatusPlanned, DeliveryOperationStatusSending,
		deliveryID, DeliveryOperationStatusRejected, DeliveryOperationStatusUnknown,
		deliveryID).Scan(&confirmedCount, &activeCount, &hardFailureCount); err != nil {
		return fmt.Errorf("complete outbound operation: aggregate: %w", err)
	}
	aggregate := DeliveryStatusSending
	switch status {
	case DeliveryOperationStatusConfirmed:
		if activeCount == 0 && hardFailureCount == 0 {
			aggregate = DeliveryStatusConfirmed
		}
	case DeliveryOperationStatusRejected:
		aggregate = DeliveryStatusRejected
		if confirmedCount > 0 {
			aggregate = DeliveryStatusPartialRejected
		}
	case DeliveryOperationStatusUnknown:
		aggregate = DeliveryStatusUnknown
		if confirmedCount > 0 {
			aggregate = DeliveryStatusPartialUnknown
		}
	}
	if _, err := tx.Exec(s.rebind(`UPDATE outbound_deliveries
		SET status = ?, confirmed_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`),
		aggregate, confirmedCount, deliveryID); err != nil {
		return fmt.Errorf("complete outbound operation: update delivery: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("complete outbound operation: commit: %w", err)
	}
	return nil
}

// ActivateOutboundDeliveryFallback records the one safe representation switch:
// a confirmed rich-format rejection to a fallback branch that was fully
// preflighted before delivery began. The rejected primary op and unsent primary
// suffix stay in the ledger as terminal audit records; fallback operations get
// fresh monotonically increasing ordinals.
func (s *Store) ActivateOutboundDeliveryFallback(deliveryID int64, rejectedOrdinal int, operations []OutboundDeliveryOperation) ([]int, error) {
	if deliveryID <= 0 || rejectedOrdinal < 0 || len(operations) == 0 {
		return nil, errors.New("activate outbound fallback: invalid delivery, rejected ordinal, or empty fallback")
	}
	localOrdinals := make(map[int]struct{}, len(operations))
	for _, op := range operations {
		if op.Ordinal < 0 || !validDeliveryOperationKind(op.Kind) {
			return nil, fmt.Errorf("activate outbound fallback: invalid operation ordinal=%d kind=%q", op.Ordinal, op.Kind)
		}
		if _, exists := localOrdinals[op.Ordinal]; exists {
			return nil, fmt.Errorf("activate outbound fallback: duplicate local ordinal %d", op.Ordinal)
		}
		localOrdinals[op.Ordinal] = struct{}{}
	}
	for ordinal := range operations {
		if _, exists := localOrdinals[ordinal]; !exists {
			return nil, fmt.Errorf("activate outbound fallback: local ordinals must be contiguous from zero (missing %d)", ordinal)
		}
	}
	ordered := append([]OutboundDeliveryOperation(nil), operations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Ordinal < ordered[j].Ordinal })

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("activate outbound fallback: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var rejectedOpID int64
	var rejectedStatus DeliveryOperationStatus
	var rejectedClass sql.NullString
	if err := tx.QueryRow(s.rebind(`SELECT id, status, error_class FROM outbound_delivery_ops
		WHERE delivery_id = ? AND ordinal = ?`), deliveryID, rejectedOrdinal).
		Scan(&rejectedOpID, &rejectedStatus, &rejectedClass); err != nil {
		return nil, fmt.Errorf("activate outbound fallback: rejected operation lookup: %w", err)
	}
	if rejectedStatus != DeliveryOperationStatusRejected || !rejectedClass.Valid || DeliveryErrorClass(rejectedClass.String) != DeliveryErrorFormat {
		return nil, fmt.Errorf("activate outbound fallback: operation is %s/%s, want rejected/format", rejectedStatus, rejectedClass.String)
	}
	var deliveryStatus DeliveryStatus
	var maxOrdinal int
	if err := tx.QueryRow(s.rebind(`SELECT d.status, COALESCE(MAX(o.ordinal), -1)
		FROM outbound_deliveries d JOIN outbound_delivery_ops o ON o.delivery_id = d.id
		WHERE d.id = ? GROUP BY d.status`), deliveryID).Scan(&deliveryStatus, &maxOrdinal); err != nil {
		return nil, fmt.Errorf("activate outbound fallback: delivery lookup: %w", err)
	}
	if deliveryStatus != DeliveryStatusRejected && deliveryStatus != DeliveryStatusPartialRejected {
		return nil, fmt.Errorf("activate outbound fallback: delivery is %s, want rejected or partial_rejected", deliveryStatus)
	}
	var unsafeCount, previousFallbackCount int
	if err := tx.QueryRow(s.rebind(`SELECT COUNT(*) FROM outbound_delivery_ops
		WHERE delivery_id = ? AND (status = ? OR (ordinal < ? AND status = ?))`),
		deliveryID, DeliveryOperationStatusSending, rejectedOrdinal, DeliveryOperationStatusPlanned).
		Scan(&unsafeCount); err != nil {
		return nil, fmt.Errorf("activate outbound fallback: inspect primary branch: %w", err)
	}
	if unsafeCount != 0 {
		return nil, errors.New("activate outbound fallback: primary branch has an unsafe sending or out-of-order operation")
	}
	if err := tx.QueryRow(s.rebind(`SELECT COUNT(*) FROM outbound_delivery_ops
		WHERE delivery_id = ? AND status = ?`), deliveryID, DeliveryOperationStatusFormatRejected).
		Scan(&previousFallbackCount); err != nil {
		return nil, fmt.Errorf("activate outbound fallback: inspect previous branches: %w", err)
	}
	if previousFallbackCount != 0 {
		return nil, errors.New("activate outbound fallback: nested or repeated fallback is not allowed")
	}
	if _, err := tx.Exec(s.rebind(`UPDATE outbound_delivery_ops SET status = ?
		WHERE id = ?`), DeliveryOperationStatusFormatRejected, rejectedOpID); err != nil {
		return nil, fmt.Errorf("activate outbound fallback: mark format rejection: %w", err)
	}
	if _, err := tx.Exec(s.rebind(`UPDATE outbound_delivery_ops
		SET status = ?, finished_at = CURRENT_TIMESTAMP
		WHERE delivery_id = ? AND ordinal > ? AND status = ?`),
		DeliveryOperationStatusSkipped, deliveryID, rejectedOrdinal, DeliveryOperationStatusPlanned); err != nil {
		return nil, fmt.Errorf("activate outbound fallback: skip primary suffix: %w", err)
	}

	newOrdinals := make([]int, 0, len(ordered))
	for i, op := range ordered {
		ordinal := maxOrdinal + 1 + i
		if _, err := tx.Exec(s.rebind(`INSERT INTO outbound_delivery_ops
			(delivery_id, ordinal, kind, status) VALUES (?, ?, ?, ?)`),
			deliveryID, ordinal, op.Kind, DeliveryOperationStatusPlanned); err != nil {
			return nil, fmt.Errorf("activate outbound fallback: append operation %d: %w", i, err)
		}
		newOrdinals = append(newOrdinals, ordinal)
	}
	if _, err := tx.Exec(s.rebind(`UPDATE outbound_deliveries SET status = ?,
		operation_count = operation_count + ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`),
		DeliveryStatusSending, len(newOrdinals), deliveryID); err != nil {
		return nil, fmt.Errorf("activate outbound fallback: update delivery: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("activate outbound fallback: commit: %w", err)
	}
	return newOrdinals, nil
}

// MarkInterruptedOutboundDeliveriesUnknown seals every nonterminal delivery at
// process restart. An operation in the non-idempotent "sending" window becomes
// unknown/interrupted; an operation still planned is known not to have started
// and becomes skipped. This also closes the crash windows before the first Mark
// and between a confirmed operation and the next Mark, so no delivery remains
// planned/sending forever after startup recovery.
func (s *Store) MarkInterruptedOutboundDeliveriesUnknown() (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("mark interrupted deliveries: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(s.rebind(`SELECT id FROM outbound_deliveries
		WHERE status IN (?, ?) ORDER BY id`), DeliveryStatusPlanned, DeliveryStatusSending)
	if err != nil {
		return 0, fmt.Errorf("mark interrupted deliveries: select: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("mark interrupted deliveries: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("mark interrupted deliveries: close rows: %w", err)
	}

	for _, id := range ids {
		var sending, confirmed int
		if err := tx.QueryRow(s.rebind(`SELECT
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0)
			FROM outbound_delivery_ops WHERE delivery_id = ?`),
			DeliveryOperationStatusSending, DeliveryOperationStatusConfirmed, id).
			Scan(&sending, &confirmed); err != nil {
			return 0, fmt.Errorf("mark interrupted deliveries: inspect operations: %w", err)
		}
		if _, err := tx.Exec(s.rebind(`UPDATE outbound_delivery_ops
			SET status = ?, error_class = ?, finished_at = CURRENT_TIMESTAMP
			WHERE delivery_id = ? AND status = ?`), DeliveryOperationStatusUnknown,
			DeliveryErrorInterrupted, id, DeliveryOperationStatusSending); err != nil {
			return 0, fmt.Errorf("mark interrupted deliveries: update operations: %w", err)
		}
		if _, err := tx.Exec(s.rebind(`UPDATE outbound_delivery_ops
			SET status = ?, finished_at = CURRENT_TIMESTAMP
			WHERE delivery_id = ? AND status = ?`), DeliveryOperationStatusSkipped,
			id, DeliveryOperationStatusPlanned); err != nil {
			return 0, fmt.Errorf("mark interrupted deliveries: skip planned operations: %w", err)
		}
		status := DeliveryStatusRejected
		if sending > 0 {
			status = DeliveryStatusUnknown
		}
		if confirmed > 0 {
			if sending > 0 {
				status = DeliveryStatusPartialUnknown
			} else {
				status = DeliveryStatusPartialRejected
			}
		}
		if _, err := tx.Exec(s.rebind(`UPDATE outbound_deliveries
			SET status = ?, confirmed_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`),
			status, confirmed, id); err != nil {
			return 0, fmt.Errorf("mark interrupted deliveries: update delivery: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("mark interrupted deliveries: commit: %w", err)
	}
	return int64(len(ids)), nil
}

func (s *Store) GetOutboundDelivery(deliveryID int64) (*OutboundDelivery, []OutboundDeliveryOperation, error) {
	var delivery OutboundDelivery
	err := s.queryRow(`SELECT id, user_id, transport, conversation_id, trace_id, history_id,
		status, operation_count, confirmed_count, created_at, updated_at
		FROM outbound_deliveries WHERE id = ?`, deliveryID).Scan(
		&delivery.ID, &delivery.UserID, &delivery.Transport, &delivery.ConversationID,
		&delivery.TraceID, &delivery.HistoryID, &delivery.Status, &delivery.OperationCount,
		&delivery.ConfirmedCount, &delivery.CreatedAt, &delivery.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get outbound delivery: %w", err)
	}

	rows, err := s.query(`SELECT id, delivery_id, ordinal, kind, status, error_class,
		started_at, finished_at, created_at FROM outbound_delivery_ops
		WHERE delivery_id = ? ORDER BY ordinal`, deliveryID)
	if err != nil {
		return nil, nil, fmt.Errorf("get outbound delivery operations: %w", err)
	}
	defer rows.Close()
	var operations []OutboundDeliveryOperation
	for rows.Next() {
		var op OutboundDeliveryOperation
		var errorClass sql.NullString
		if err := rows.Scan(&op.ID, &op.DeliveryID, &op.Ordinal, &op.Kind, &op.Status,
			&errorClass, &op.StartedAt, &op.FinishedAt, &op.CreatedAt); err != nil {
			return nil, nil, fmt.Errorf("get outbound delivery operations: scan: %w", err)
		}
		if errorClass.Valid {
			op.ErrorClass = DeliveryErrorClass(errorClass.String)
		}
		operations = append(operations, op)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("get outbound delivery operations: iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, fmt.Errorf("get outbound delivery operations: close: %w", err)
	}
	// Close the operation cursor before issuing nested reads. SQLite deliberately
	// uses one connection, so querying while rows is open would deadlock.
	for i := range operations {
		messageRows, err := s.query(`SELECT message_id FROM outbound_delivery_messages
			WHERE op_id = ? ORDER BY ordinal`, operations[i].ID)
		if err != nil {
			return nil, nil, fmt.Errorf("get outbound delivery messages: %w", err)
		}
		for messageRows.Next() {
			var id string
			if err := messageRows.Scan(&id); err != nil {
				_ = messageRows.Close()
				return nil, nil, fmt.Errorf("get outbound delivery messages: scan: %w", err)
			}
			operations[i].TransportMessageIDs = append(operations[i].TransportMessageIDs, id)
		}
		if err := messageRows.Err(); err != nil {
			_ = messageRows.Close()
			return nil, nil, fmt.Errorf("get outbound delivery messages: iterate: %w", err)
		}
		if err := messageRows.Close(); err != nil {
			return nil, nil, fmt.Errorf("get outbound delivery messages: close: %w", err)
		}
	}
	return &delivery, operations, nil
}

// GetOutboundDeliveryByTransportMessage resolves a stable ID recorded for a
// confirmed operation even when the logical reply has not acquired a history
// row (for example a confirmed prefix followed by an unknown operation, or the
// crash window immediately after final delivery confirmation). The query is
// content-free and exact across scope, transport, conversation, and message.
func (s *Store) GetOutboundDeliveryByTransportMessage(userID ScopeID, transport, conversationID, transportMsgID string) (*OutboundDelivery, error) {
	if userID == "" || strings.TrimSpace(transport) == "" || strings.TrimSpace(conversationID) == "" || strings.TrimSpace(transportMsgID) == "" {
		return nil, errors.New("get outbound delivery by transport message: user, transport, conversation, and message are required")
	}
	rows, err := s.query(`SELECT d.id, d.user_id, d.transport, d.conversation_id,
		d.trace_id, d.history_id, d.status, d.operation_count, d.confirmed_count,
		d.created_at, d.updated_at
		FROM outbound_delivery_messages m
		JOIN outbound_delivery_ops o
		  ON o.id = m.op_id AND o.delivery_id = m.delivery_id
		JOIN outbound_deliveries d ON d.id = m.delivery_id
		WHERE d.user_id = ? AND d.transport = ? AND d.conversation_id = ?
		  AND m.message_id = ? AND o.status = ?
		ORDER BY d.id DESC LIMIT 2`, userID, transport, conversationID, transportMsgID,
		DeliveryOperationStatusConfirmed)
	if err != nil {
		return nil, fmt.Errorf("get outbound delivery by transport message: %w", err)
	}
	defer rows.Close()

	var delivery OutboundDelivery
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get outbound delivery by transport message: iterate: %w", err)
		}
		return nil, nil
	}
	if err := rows.Scan(&delivery.ID, &delivery.UserID, &delivery.Transport,
		&delivery.ConversationID, &delivery.TraceID, &delivery.HistoryID,
		&delivery.Status, &delivery.OperationCount, &delivery.ConfirmedCount,
		&delivery.CreatedAt, &delivery.UpdatedAt); err != nil {
		return nil, fmt.Errorf("get outbound delivery by transport message: scan: %w", err)
	}
	if rows.Next() {
		return nil, errors.New("get outbound delivery by transport message: ambiguous duplicate transport identity")
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get outbound delivery by transport message: iterate: %w", err)
	}
	return &delivery, nil
}

func validDeliveryErrorClass(class DeliveryErrorClass) bool {
	switch class {
	case DeliveryErrorNone, DeliveryErrorFormat, DeliveryErrorRateLimit,
		DeliveryErrorNetwork, DeliveryErrorServer, DeliveryErrorInvalidResponse,
		DeliveryErrorInternal, DeliveryErrorInterrupted:
		return true
	default:
		return false
	}
}

func validDeliveryOperationKind(kind DeliveryOperationKind) bool {
	switch kind {
	case DeliveryOperationRichText, DeliveryOperationLegacyText,
		DeliveryOperationRichMedia, DeliveryOperationMedia:
		return true
	default:
		return false
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) insertReturningIDTx(tx *sql.Tx, query, idCol string, args ...any) (int64, error) {
	if s.dialect.Name() == "postgres" {
		var id int64
		err := tx.QueryRow(s.rebind(query+" RETURNING "+idCol), args...).Scan(&id)
		return id, err
	}
	result, err := tx.Exec(s.rebind(query), args...)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// LinkReplyTransportMessages links a fully materialized list to an exact
// assistant history row. It is atomic and idempotent for the same mapping.
func (s *Store) LinkReplyTransportMessages(userID ScopeID, historyID int64, messages []TransportMessage) error {
	if err := validateTransportMessages(messages); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("link reply transport messages: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.linkReplyTransportMessagesTx(tx, userID, historyID, messages); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("link reply transport messages: commit: %w", err)
	}
	return nil
}

func validateTransportMessages(messages []TransportMessage) error {
	if len(messages) == 0 {
		return errors.New("link reply transport messages: at least one message is required")
	}
	ordinals := make(map[int]struct{}, len(messages))
	identities := make(map[string]struct{}, len(messages))
	primaryCount := 0
	for _, message := range messages {
		if strings.TrimSpace(message.Transport) == "" || strings.TrimSpace(message.ConversationID) == "" || strings.TrimSpace(message.MessageID) == "" || message.Ordinal < 0 {
			return fmt.Errorf("link reply transport messages: invalid message %+v", message)
		}
		if _, exists := ordinals[message.Ordinal]; exists {
			return fmt.Errorf("link reply transport messages: duplicate ordinal %d", message.Ordinal)
		}
		ordinals[message.Ordinal] = struct{}{}
		identity := message.Transport + "\x00" + message.ConversationID + "\x00" + message.MessageID
		if _, exists := identities[identity]; exists {
			return fmt.Errorf("link reply transport messages: duplicate transport identity")
		}
		identities[identity] = struct{}{}
		if message.IsPrimary {
			primaryCount++
		}
	}
	if primaryCount != 1 {
		return fmt.Errorf("link reply transport messages: expected exactly one primary message, got %d", primaryCount)
	}
	return nil
}

func (s *Store) linkReplyTransportMessagesTx(tx *sql.Tx, userID ScopeID, historyID int64, messages []TransportMessage) error {
	if historyID <= 0 || userID == "" {
		return errors.New("link reply transport messages: invalid history id or user")
	}
	var role string
	var legacyMessageID, legacyConversationID sql.NullString
	if err := tx.QueryRow(s.rebind(`SELECT role, message_id, conversation_id FROM history
		WHERE id = ? AND user_id = ?`), historyID, userID).
		Scan(&role, &legacyMessageID, &legacyConversationID); err != nil {
		return fmt.Errorf("link reply transport messages: history lookup: %w", err)
	}
	if role != "assistant" {
		return errors.New("link reply transport messages: history row is not an assistant reply")
	}

	ordered := append([]TransportMessage(nil), messages...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Ordinal < ordered[j].Ordinal })
	var primary TransportMessage
	for _, message := range ordered {
		if message.IsPrimary {
			primary = message
		}
		var existingHistoryID int64
		var existingOrdinal int
		var existingPrimary bool
		err := tx.QueryRow(s.rebind(`SELECT history_id, ordinal, is_primary
			FROM history_transport_messages
			WHERE user_id = ? AND transport = ? AND conversation_id = ? AND message_id = ?`),
			userID, message.Transport, message.ConversationID, message.MessageID).
			Scan(&existingHistoryID, &existingOrdinal, &existingPrimary)
		switch {
		case err == nil:
			if existingHistoryID != historyID || existingOrdinal != message.Ordinal || existingPrimary != message.IsPrimary {
				return fmt.Errorf("link reply transport messages: transport identity %s/%s/%s is already linked differently", message.Transport, message.ConversationID, message.MessageID)
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("link reply transport messages: mapping lookup: %w", err)
		}
		if _, err := tx.Exec(s.rebind(`INSERT INTO history_transport_messages
			(history_id, user_id, transport, conversation_id, message_id, ordinal, is_primary)
			VALUES (?, ?, ?, ?, ?, ?, ?)`), historyID, userID, message.Transport,
			message.ConversationID, message.MessageID, message.Ordinal, message.IsPrimary); err != nil {
			return fmt.Errorf("link reply transport messages: insert mapping: %w", err)
		}
	}

	if legacyMessageID.Valid && legacyMessageID.String != primary.MessageID {
		return fmt.Errorf("link reply transport messages: history row already has message id %q", legacyMessageID.String)
	}
	if legacyConversationID.Valid && legacyConversationID.String != primary.ConversationID {
		return fmt.Errorf("link reply transport messages: history row already has conversation id %q", legacyConversationID.String)
	}
	if _, err := tx.Exec(s.rebind(`UPDATE history SET message_id = ?, conversation_id = ?
		WHERE id = ? AND user_id = ?`), primary.MessageID, primary.ConversationID, historyID, userID); err != nil {
		return fmt.Errorf("link reply transport messages: mirror primary id: %w", err)
	}
	return nil
}

func (s *Store) GetReplyByTransportMessage(userID ScopeID, transport, conversationID, transportMsgID string) (*Message, error) {
	if userID == "" || strings.TrimSpace(transport) == "" || strings.TrimSpace(conversationID) == "" || strings.TrimSpace(transportMsgID) == "" {
		return nil, errors.New("get reply by transport message: user, transport, conversation, and message are required")
	}
	query := `SELECT h.id, h.content, h.trace_id FROM history_transport_messages tm
		JOIN history h ON h.id = tm.history_id AND h.user_id = tm.user_id
		WHERE tm.user_id = ? AND tm.transport = ? AND tm.conversation_id = ?
		  AND tm.message_id = ? AND h.role = 'assistant'
		LIMIT 1`
	msg, err := s.scanReply(s.queryRow(query, userID, transport, conversationID, transportMsgID), userID)
	if err != nil || msg != nil {
		return msg, err
	}

	// Pre-v19 fallback: exact conversation first, then NULL conversation rows
	// from Telegram's former single-scope path. History did not retain transport,
	// so this compatibility read is safe only for Telegram's numeric chat IDs.
	// Cross-transport principal scopes must never let a Mattermost post collide
	// with a legacy Telegram message merely because both native strings match.
	if transport != "telegram" {
		return nil, nil
	}
	if _, parseErr := strconv.ParseInt(conversationID, 10, 64); parseErr != nil {
		return nil, nil
	}
	legacy := `SELECT id, content, trace_id FROM history
		WHERE user_id = ? AND message_id = ? AND role = 'assistant'
		  AND (conversation_id = ? OR conversation_id IS NULL)
		ORDER BY CASE WHEN conversation_id = ? THEN 0 ELSE 1 END, id DESC LIMIT 1`
	return s.scanReply(s.queryRow(legacy, userID, transportMsgID, conversationID, conversationID), userID)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanReply(row rowScanner, userID ScopeID) (*Message, error) {
	msg := &Message{UserID: userID, Role: "assistant"}
	err := row.Scan(&msg.ID, &msg.Content, &msg.TraceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return msg, nil
}

func (s *Store) LinkOutboundDeliveryHistory(userID ScopeID, deliveryID, historyID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("link outbound delivery history: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.linkOutboundDeliveryHistoryTx(tx, userID, deliveryID, historyID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("link outbound delivery history: commit: %w", err)
	}
	return nil
}

func (s *Store) linkOutboundDeliveryHistoryTx(tx *sql.Tx, userID ScopeID, deliveryID, historyID int64) error {
	var transport, conversationID string
	var status DeliveryStatus
	var existingHistoryID sql.NullInt64
	err := tx.QueryRow(s.rebind(`SELECT transport, conversation_id, status, history_id
		FROM outbound_deliveries WHERE id = ? AND user_id = ?`), deliveryID, userID).
		Scan(&transport, &conversationID, &status, &existingHistoryID)
	if err != nil {
		return fmt.Errorf("link outbound delivery history: delivery lookup: %w", err)
	}
	if existingHistoryID.Valid && existingHistoryID.Int64 != historyID {
		return fmt.Errorf("link outbound delivery history: delivery already linked to history %d", existingHistoryID.Int64)
	}
	if status != DeliveryStatusConfirmed && status != DeliveryStatusPartialRejected && status != DeliveryStatusPartialUnknown {
		return fmt.Errorf("link outbound delivery history: delivery status %q has no confirmed reply", status)
	}

	rows, err := tx.Query(s.rebind(`SELECT m.message_id FROM outbound_delivery_messages m
		JOIN outbound_delivery_ops o ON o.id = m.op_id
		WHERE m.delivery_id = ? AND o.status = ?
		ORDER BY o.ordinal, m.ordinal`), deliveryID, DeliveryOperationStatusConfirmed)
	if err != nil {
		return fmt.Errorf("link outbound delivery history: messages query: %w", err)
	}
	var messages []TransportMessage
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("link outbound delivery history: message scan: %w", err)
		}
		messages = append(messages, TransportMessage{
			Transport: transport, ConversationID: conversationID, MessageID: id,
			Ordinal: len(messages), IsPrimary: len(messages) == 0,
		})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("link outbound delivery history: iterate messages: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("link outbound delivery history: close messages: %w", err)
	}
	if err := validateTransportMessages(messages); err != nil {
		return fmt.Errorf("link outbound delivery history: %w", err)
	}
	if err := s.linkReplyTransportMessagesTx(tx, userID, historyID, messages); err != nil {
		return err
	}
	if _, err := tx.Exec(s.rebind(`UPDATE outbound_deliveries SET history_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND user_id = ?`), historyID, deliveryID, userID); err != nil {
		return fmt.Errorf("link outbound delivery history: update delivery: %w", err)
	}
	return nil
}

func (s *Store) PersistOutboundDeliveryReply(userID ScopeID, deliveryID int64, message Message, artifactIDs []int64) (int64, error) {
	references := make([]OutboundArtifactReference, len(artifactIDs))
	for i, artifactID := range artifactIDs {
		references[i] = OutboundArtifactReference{
			ArtifactID: artifactID,
			Ordinal:    i,
			Mode:       artifactdelivery.ModePreview,
			Source:     ArtifactReferenceSourceGenerated,
		}
	}
	return s.persistOutboundDeliveryReply(userID, deliveryID, message, PersistOutboundArtifacts{
		OwnedArtifactIDs: artifactIDs,
		References:       references,
	}, true)
}

// PersistOutboundDeliveryReplyWithArtifacts is the provenance-preserving V2
// persistence path. It atomically creates the assistant history row, links the
// confirmed transport messages and delivery, assigns only in-flight
// (message_id=0) artifacts to their creator reply, and records ordered M:N
// references for every delivered generated or stored artifact.
func (s *Store) PersistOutboundDeliveryReplyWithArtifacts(userID ScopeID, deliveryID int64, message Message, artifacts PersistOutboundArtifacts) (int64, error) {
	return s.persistOutboundDeliveryReply(userID, deliveryID, message, artifacts, false)
}

func (s *Store) persistOutboundDeliveryReply(userID ScopeID, deliveryID int64, message Message, artifacts PersistOutboundArtifacts, allowOwnerRebind bool) (int64, error) {
	if message.Role != "assistant" {
		return 0, errors.New("persist outbound delivery reply: message role must be assistant")
	}
	normalized, err := normalizePersistOutboundArtifacts(artifacts)
	if err != nil {
		return 0, fmt.Errorf("persist outbound delivery reply: %w", err)
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("persist outbound delivery reply: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := `INSERT INTO history (user_id, role, content, topic_id, created_at, author,
		message_id, conversation_id, thread_root, trace_id, do_not_store)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	historyID, err := s.insertReturningIDTx(tx, query, "id", userID, message.Role,
		message.Content, message.TopicID, s.dialect.BindTime(message.CreatedAt), message.Author,
		message.MessageID, message.ConversationID, message.ThreadRoot, message.TraceID, message.DoNotStore)
	if err != nil {
		return 0, fmt.Errorf("persist outbound delivery reply: insert history: %w", err)
	}
	if err := s.linkOutboundDeliveryHistoryTx(tx, userID, deliveryID, historyID); err != nil {
		return 0, err
	}
	verifiedArtifacts := make(map[int64]struct{}, len(normalized.OwnedArtifactIDs)+len(normalized.References))
	for _, artifactID := range normalized.OwnedArtifactIDs {
		// Artifact rows are deduplicated by (user_id, content_hash), so an image
		// generated again may already belong to an older history row. The legacy
		// API preserves its historical rebind behavior; the V2 API accepts only a
		// genuinely in-flight row here and requires reused artifacts as references.
		ownerPredicate := ""
		if !allowOwnerRebind {
			ownerPredicate = " AND message_id = 0"
		}
		result, err := tx.Exec(s.rebind(`UPDATE artifacts SET message_id = ?
			WHERE user_id = ? AND id = ?`+ownerPredicate), historyID, userID, artifactID)
		if err != nil {
			return 0, fmt.Errorf("persist outbound delivery reply: link artifact %d: %w", artifactID, err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("persist outbound delivery reply: count artifact %d: %w", artifactID, err)
		}
		if updated != 1 {
			if !allowOwnerRebind {
				var existingMessageID int64
				lookupErr := tx.QueryRow(s.rebind(`SELECT message_id FROM artifacts WHERE user_id = ? AND id = ?`), userID, artifactID).Scan(&existingMessageID)
				if lookupErr == nil {
					return 0, fmt.Errorf("persist outbound delivery reply: artifact %d already has creator history %d; persist it as a reference", artifactID, existingMessageID)
				}
				if lookupErr != sql.ErrNoRows {
					return 0, fmt.Errorf("persist outbound delivery reply: inspect artifact %d: %w", artifactID, lookupErr)
				}
			}
			return 0, fmt.Errorf("persist outbound delivery reply: artifact %d not found or owned by another user", artifactID)
		}
		verifiedArtifacts[artifactID] = struct{}{}
	}
	for _, reference := range normalized.References {
		if _, verified := verifiedArtifacts[reference.ArtifactID]; !verified {
			var one int
			err := tx.QueryRow(s.rebind(`SELECT 1 FROM artifacts WHERE user_id = ? AND id = ?`), userID, reference.ArtifactID).Scan(&one)
			if err == sql.ErrNoRows {
				return 0, fmt.Errorf("persist outbound delivery reply: artifact reference %d not found or owned by another user", reference.ArtifactID)
			}
			if err != nil {
				return 0, fmt.Errorf("persist outbound delivery reply: inspect artifact reference %d: %w", reference.ArtifactID, err)
			}
			verifiedArtifacts[reference.ArtifactID] = struct{}{}
		}
		if _, err := tx.Exec(s.rebind(`INSERT INTO history_artifact_refs
			(history_id, user_id, artifact_id, ordinal, mode, source_kind)
			VALUES (?, ?, ?, ?, ?, ?)`), historyID, userID, reference.ArtifactID,
			reference.Ordinal, reference.Mode, reference.Source); err != nil {
			return 0, fmt.Errorf("persist outbound delivery reply: insert artifact reference %d at ordinal %d: %w", reference.ArtifactID, reference.Ordinal, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("persist outbound delivery reply: commit: %w", err)
	}
	return historyID, nil
}

func normalizePersistOutboundArtifacts(artifacts PersistOutboundArtifacts) (PersistOutboundArtifacts, error) {
	normalized := PersistOutboundArtifacts{
		OwnedArtifactIDs: append([]int64(nil), artifacts.OwnedArtifactIDs...),
		References:       append([]OutboundArtifactReference(nil), artifacts.References...),
	}
	owned := make(map[int64]struct{}, len(normalized.OwnedArtifactIDs))
	for _, artifactID := range normalized.OwnedArtifactIDs {
		if artifactID <= 0 {
			return PersistOutboundArtifacts{}, fmt.Errorf("invalid owned artifact id %d", artifactID)
		}
		if _, exists := owned[artifactID]; exists {
			return PersistOutboundArtifacts{}, fmt.Errorf("duplicate owned artifact id %d", artifactID)
		}
		owned[artifactID] = struct{}{}
	}
	ordinals := make(map[int]struct{}, len(normalized.References))
	referenced := make(map[int64]struct{}, len(normalized.References))
	for i := range normalized.References {
		reference := &normalized.References[i]
		if reference.ArtifactID <= 0 {
			return PersistOutboundArtifacts{}, fmt.Errorf("invalid artifact reference id %d", reference.ArtifactID)
		}
		if reference.Ordinal < 0 {
			return PersistOutboundArtifacts{}, fmt.Errorf("invalid artifact reference ordinal %d", reference.Ordinal)
		}
		if _, exists := ordinals[reference.Ordinal]; exists {
			return PersistOutboundArtifacts{}, fmt.Errorf("duplicate artifact reference ordinal %d", reference.Ordinal)
		}
		ordinals[reference.Ordinal] = struct{}{}

		var (
			mode artifactdelivery.Mode
			err  error
		)
		switch reference.Source {
		case ArtifactReferenceSourceGenerated:
			mode, err = artifactdelivery.ParseGeneratedMode(string(reference.Mode))
		case ArtifactReferenceSourceStored:
			mode, err = artifactdelivery.ParseStoredMode(string(reference.Mode))
		default:
			return PersistOutboundArtifacts{}, fmt.Errorf("unsupported artifact reference source %q", reference.Source)
		}
		if err != nil {
			return PersistOutboundArtifacts{}, fmt.Errorf("artifact reference %d: %w", reference.ArtifactID, err)
		}
		reference.Mode = mode
		referenced[reference.ArtifactID] = struct{}{}
	}
	for artifactID := range owned {
		if _, exists := referenced[artifactID]; !exists {
			return PersistOutboundArtifacts{}, fmt.Errorf("owned artifact %d has no delivery reference", artifactID)
		}
	}
	return normalized, nil
}

// GetHistoryArtifactReferences returns the provenance-preserving associations
// for one logical reply. Ordering is explicit and independent of insert order.
func (s *Store) GetHistoryArtifactReferences(userID ScopeID, historyID int64) ([]HistoryArtifactReference, error) {
	if userID == "" {
		return nil, errors.New("get history artifact references: user id is required")
	}
	if historyID <= 0 {
		return nil, fmt.Errorf("get history artifact references: invalid history id %d", historyID)
	}
	rows, err := s.query(`SELECT r.history_id, r.user_id, r.artifact_id, r.ordinal, r.mode, r.source_kind, r.created_at
		FROM history_artifact_refs r
		JOIN history h ON h.id = r.history_id
		JOIN artifacts a ON a.id = r.artifact_id
		WHERE r.user_id = ? AND h.user_id = ? AND a.user_id = ? AND r.history_id = ?
		ORDER BY r.ordinal, r.id`, userID, userID, userID, historyID)
	if err != nil {
		return nil, fmt.Errorf("get history artifact references: query: %w", err)
	}
	defer rows.Close()

	var references []HistoryArtifactReference
	for rows.Next() {
		var (
			reference HistoryArtifactReference
			mode      string
			source    string
		)
		if err := rows.Scan(&reference.HistoryID, &reference.UserID, &reference.ArtifactID,
			&reference.Ordinal, &mode, &source, &reference.CreatedAt); err != nil {
			return nil, fmt.Errorf("get history artifact references: scan: %w", err)
		}
		reference.Mode = artifactdelivery.Mode(mode)
		reference.Source = ArtifactReferenceSource(source)
		references = append(references, reference)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get history artifact references: iterate: %w", err)
	}
	return references, nil
}
