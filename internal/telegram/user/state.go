package user

import (
	"context"
	"database/sql"

	"github.com/gotd/td/telegram/updates"
)

type UpdateStateStorage struct {
	db *sql.DB
}

func NewUpdateStateStorage(db *sql.DB) *UpdateStateStorage {
	return &UpdateStateStorage{db: db}
}

func (s *UpdateStateStorage) GetState(ctx context.Context, userID int64) (updates.State, bool, error) {
	var state updates.State
	err := s.db.QueryRowContext(ctx, `
		SELECT pts, qts, date, seq FROM telegram_update_state WHERE user_id=?`, userID).
		Scan(&state.Pts, &state.Qts, &state.Date, &state.Seq)
	if err == sql.ErrNoRows {
		return updates.State{}, false, nil
	}
	return state, err == nil, err
}

func (s *UpdateStateStorage) SetState(ctx context.Context, userID int64, state updates.State) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telegram_update_state(user_id, pts, qts, date, seq) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET pts=excluded.pts, qts=excluded.qts, date=excluded.date, seq=excluded.seq`,
		userID, state.Pts, state.Qts, state.Date, state.Seq)
	return err
}

func (s *UpdateStateStorage) SetPts(ctx context.Context, userID int64, pts int) error {
	result, err := s.db.ExecContext(ctx, `UPDATE telegram_update_state SET pts=? WHERE user_id=?`, pts, userID)
	return requireUpdatedRow(result, err)
}

func (s *UpdateStateStorage) SetQts(ctx context.Context, userID int64, qts int) error {
	result, err := s.db.ExecContext(ctx, `UPDATE telegram_update_state SET qts=? WHERE user_id=?`, qts, userID)
	return requireUpdatedRow(result, err)
}

func (s *UpdateStateStorage) SetDate(ctx context.Context, userID int64, date int) error {
	result, err := s.db.ExecContext(ctx, `UPDATE telegram_update_state SET date=? WHERE user_id=?`, date, userID)
	return requireUpdatedRow(result, err)
}

func (s *UpdateStateStorage) SetSeq(ctx context.Context, userID int64, seq int) error {
	result, err := s.db.ExecContext(ctx, `UPDATE telegram_update_state SET seq=? WHERE user_id=?`, seq, userID)
	return requireUpdatedRow(result, err)
}

func (s *UpdateStateStorage) SetDateSeq(ctx context.Context, userID int64, date, seq int) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE telegram_update_state SET date=?, seq=? WHERE user_id=?`, date, seq, userID)
	if err != nil {
		return err
	}
	return requireUpdatedRow(result, err)
}

func (s *UpdateStateStorage) GetChannelPts(ctx context.Context, userID, channelID int64) (int, bool, error) {
	var pts int
	err := s.db.QueryRowContext(ctx, `
		SELECT pts FROM telegram_channel_state WHERE user_id=? AND channel_id=?`, userID, channelID).Scan(&pts)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return pts, err == nil, err
}

func (s *UpdateStateStorage) SetChannelPts(ctx context.Context, userID, channelID int64, pts int) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telegram_channel_state(user_id, channel_id, pts) VALUES(?, ?, ?)
		ON CONFLICT(user_id, channel_id) DO UPDATE SET pts=excluded.pts`, userID, channelID, pts)
	return err
}

func (s *UpdateStateStorage) ForEachChannels(ctx context.Context, userID int64, f func(context.Context, int64, int) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT channel_id, pts FROM telegram_channel_state WHERE user_id=?`, userID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var channelID int64
		var pts int
		if err := rows.Scan(&channelID, &pts); err != nil {
			return err
		}
		if err := f(ctx, channelID, pts); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *UpdateStateStorage) SetChannelAccessHash(ctx context.Context, userID, channelID, accessHash int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telegram_channel_state(user_id, channel_id, pts, access_hash) VALUES(?, ?, 0, ?)
		ON CONFLICT(user_id, channel_id) DO UPDATE SET access_hash=excluded.access_hash`,
		userID, channelID, accessHash)
	return err
}

func (s *UpdateStateStorage) GetChannelAccessHash(ctx context.Context, userID, channelID int64) (int64, bool, error) {
	var hash int64
	err := s.db.QueryRowContext(ctx, `
		SELECT access_hash FROM telegram_channel_state WHERE user_id=? AND channel_id=? AND access_hash != 0`,
		userID, channelID).Scan(&hash)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return hash, err == nil, err
}

func (s *UpdateStateStorage) SetUserAccessHash(ctx context.Context, userID, targetUserID, accessHash int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telegram_user_hashes(user_id, target_user_id, access_hash) VALUES(?, ?, ?)
		ON CONFLICT(user_id, target_user_id) DO UPDATE SET access_hash=excluded.access_hash`,
		userID, targetUserID, accessHash)
	return err
}

func (s *UpdateStateStorage) GetUserAccessHash(ctx context.Context, userID, targetUserID int64) (int64, bool, error) {
	var hash int64
	err := s.db.QueryRowContext(ctx, `
		SELECT access_hash FROM telegram_user_hashes WHERE user_id=? AND target_user_id=?`, userID, targetUserID).
		Scan(&hash)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return hash, err == nil, err
}

func requireUpdatedRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	changed, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return rowsErr
	}
	if changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}
