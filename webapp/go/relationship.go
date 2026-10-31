package isureal

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/jmoiron/sqlx"
)

func beginRelationshipTx(ctx context.Context, db *sqlx.DB, actor, target string) (*sqlx.Tx, error) {
	if actor == target || target == "" {
		return nil, NewAPIError(http.StatusBadRequest, "invalid_relationship")
	}
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// 同じ2人の関係変更だけを advisory lock で直列化する。人気のある利用者に申請が
	// 集中しても待たないよう、利用者の行は共有ロックにする。退会やコメントが取る
	// FOR NO KEY UPDATE とは競合するので、それらとの整合性は保たれる。
	var lockedIDs []string
	if err := tx.SelectContext(ctx, &lockedIDs, `
  WITH pair AS MATERIALIZED (SELECT pg_advisory_xact_lock(hashtextextended($3, 0)))
  SELECT u.id FROM users u, pair
  WHERE u.id IN ($1, $2) AND u.status='active'
  ORDER BY u.id COLLATE "C"
  FOR SHARE OF u
 `, actor, target, min(actor, target)+":"+max(actor, target)); err != nil {
		tx.Rollback()
		return nil, err
	}
	if len(lockedIDs) != 2 {
		tx.Rollback()
		return nil, NewAPIError(http.StatusNotFound, "relationship_not_found")
	}
	return tx, nil
}

func isBlocked(ctx context.Context, db sqlx.QueryerContext, actor, target string) (bool, error) {
	var blocked bool
	err := sqlx.GetContext(ctx, db, &blocked, `SELECT `+blockedBetweenQuery, actor, target)
	return blocked, err
}

const blockedBetweenQuery = `EXISTS(SELECT 1 FROM block_relations WHERE (blocker_id=$1 AND blocked_id=$2) OR (blocker_id=$2 AND blocked_id=$1))`

func requireRelationshipChange(result sql.Result, err error, status int, code string) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return NewAPIError(status, code)
	}
	return nil
}
