package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// auditChainBatch is how many rows the backfill and the verification read
// at a time: the walk never holds the whole log in memory, and between
// batches the single connection is free for other work.
const auditChainBatch = 1000

// AuditHash is an audit entry's hash in the chain (SEC-45): hex SHA-256 of
// prev, ts (as stored), username, ip, action, target_kind, target_id
// (decimal), detail_json and outcome, joined by "\n". The first entry's
// prev is "".
func AuditHash(prev string, e AuditEntry) string {
	return auditHashRaw(prev, formatTime(e.TS), e.Username, e.IP, e.Action, e.TargetKind,
		strconv.FormatInt(e.TargetID, 10), e.DetailJSON, e.Outcome)
}

// auditHashRaw hashes the fields as stored, so that verifying a row never
// depends on re-parsing its timestamp.
func auditHashRaw(fields ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(fields, "\n")))
	return hex.EncodeToString(sum[:])
}

// auditChainRow is one audit row as the chain sees it.
type auditChainRow struct {
	id                                    int64
	ts, username, ip, action, targetKind  string
	targetID                              int64
	detail, outcome, prevHash, storedHash string
}

func (r auditChainRow) hashOver(prev string) string {
	return auditHashRaw(prev, r.ts, r.username, r.ip, r.action, r.targetKind,
		strconv.FormatInt(r.targetID, 10), r.detail, r.outcome)
}

// querier is what the chain walk reads through: the store's database or a
// migration's transaction.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// auditChainBatchAfter returns up to auditChainBatch rows with an id above
// after, in id order.
func auditChainBatchAfter(q querier, after int64) ([]auditChainRow, error) {
	rows, err := q.Query(`SELECT id, ts, username, ip, action, target_kind, target_id, detail_json, outcome, prev_hash, hash
		FROM audit_log WHERE id > ? ORDER BY id LIMIT ?`, after, auditChainBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]auditChainRow, 0, auditChainBatch)
	for rows.Next() {
		var r auditChainRow
		if err := rows.Scan(&r.id, &r.ts, &r.username, &r.ip, &r.action, &r.targetKind, &r.targetID,
			&r.detail, &r.outcome, &r.prevHash, &r.storedHash); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// VerifyAuditChain walks the audit log in id order and checks each row's
// prev_hash and hash. lastOK is the id of the last row of the intact part
// (0 when none); brokenAt is the id of the first row that does not fit the
// chain, 0 when the whole chain is intact. A row changed, inserted or
// removed in the middle breaks the chain at that row or the next one.
func (s *Store) VerifyAuditChain() (lastOK int64, brokenAt int64, err error) {
	prev := ""
	for {
		batch, err := auditChainBatchAfter(s.db, lastOK)
		if err != nil {
			return lastOK, 0, fmt.Errorf("verify audit chain: %w", err)
		}
		for _, r := range batch {
			if r.prevHash != prev || r.storedHash != r.hashOver(prev) {
				return lastOK, r.id, nil
			}
			prev, lastOK = r.storedHash, r.id
		}
		if len(batch) < auditChainBatch {
			return lastOK, 0, nil
		}
	}
}

// Statements of the append-only triggers (as 0004_monitoring.sql creates
// them). Only migration 0009 lifts and restores them.
const (
	dropAuditTriggers   = `DROP TRIGGER audit_log_no_update; DROP TRIGGER audit_log_no_delete;`
	createAuditTriggers = `
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;`
)

// backfillAuditChain is migration 0009's hook, run in its transaction
// after the columns are added: it lifts the append-only triggers, chains
// the existing rows in id order and restores the triggers. A failure
// rolls the whole migration back.
func backfillAuditChain(tx *sql.Tx) error {
	if _, err := tx.Exec(dropAuditTriggers); err != nil {
		return fmt.Errorf("drop audit triggers: %w", err)
	}
	stmt, err := tx.Prepare(`UPDATE audit_log SET prev_hash = ?, hash = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	prev, after := "", int64(0)
	for {
		batch, err := auditChainBatchAfter(tx, after)
		if err != nil {
			return fmt.Errorf("read audit rows: %w", err)
		}
		for _, r := range batch {
			h := r.hashOver(prev)
			if _, err := stmt.Exec(prev, h, r.id); err != nil {
				return fmt.Errorf("chain audit row %d: %w", r.id, err)
			}
			prev, after = h, r.id
		}
		if len(batch) < auditChainBatch {
			break
		}
	}
	if _, err := tx.Exec(createAuditTriggers); err != nil {
		return fmt.Errorf("restore audit triggers: %w", err)
	}
	return nil
}
