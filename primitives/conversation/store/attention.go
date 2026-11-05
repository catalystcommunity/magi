package store

import (
	"context"

	api "github.com/catalystcommunity/magi/primitives/conversation/api"
	"github.com/jackc/pgx/v5"
)

// Devices and notices. The service checks the person; these tables have
// no row-level security.

// NoticeKey is the NOTIFY payload (on Channel) for a new or changed notice
// of person.
func NoticeKey(person string) string { return "notice:" + person }

const devCols = "person, id, kind, name, can, active, hands_free, seen_ms, created_ms"

func scanDevice(r pgx.Row) (api.Device, error) {
	var d api.Device
	var seen, created int64
	err := r.Scan(&d.Person, &d.Id, &d.Kind, &d.Name, &d.Can, &d.Active, &d.HandsFree, &seen, &created)
	d.SeenMs, d.CreatedMs = uint64(seen), uint64(created)
	if d.Can == nil {
		d.Can = []string{}
	}
	return d, err
}

// PutDevice adds the device or updates it; created_ms stays.
func (db *DB) PutDevice(ctx context.Context, d api.Device) (api.Device, error) {
	if d.Can == nil {
		d.Can = []string{}
	}
	return one(scanDevice(db.pool.QueryRow(ctx, `INSERT INTO devices (`+devCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
ON CONFLICT (person, id) DO UPDATE SET kind = EXCLUDED.kind, name = EXCLUDED.name, can = EXCLUDED.can,
  active = EXCLUDED.active, hands_free = EXCLUDED.hands_free, seen_ms = EXCLUDED.seen_ms
RETURNING `+devCols, d.Person, d.Id, d.Kind, d.Name, d.Can, d.Active, d.HandsFree, int64(d.SeenMs))))
}

// TouchDevice sets seen_ms of a device that waits for notices.
func (db *DB) TouchDevice(ctx context.Context, person, id string, ms uint64) error {
	_, err := db.pool.Exec(ctx, `UPDATE devices SET seen_ms = GREATEST(seen_ms, $3) WHERE person = $1 AND id = $2`, person, id, int64(ms))
	return err
}

func (db *DB) GetDevice(ctx context.Context, person, id string) (api.Device, error) {
	return one(scanDevice(db.pool.QueryRow(ctx, `SELECT `+devCols+` FROM devices WHERE person = $1 AND id = $2`, person, id)))
}

func (db *DB) ListDevices(ctx context.Context, person string) ([]api.Device, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+devCols+` FROM devices WHERE person = $1 ORDER BY seen_ms DESC, id`, person)
	return many(rows, err, scanDevice)
}

func (db *DB) RemoveDevice(ctx context.Context, person, id string) (api.Device, error) {
	return one(scanDevice(db.pool.QueryRow(ctx, `DELETE FROM devices WHERE person = $1 AND id = $2 RETURNING `+devCols, person, id)))
}

const noticeCols = "id, person, persona, stream, utterance, title, text, urgency, device, created_ms, acked_ms, acked_by"

func scanNotice(r pgx.Row) (api.Notice, error) {
	var n api.Notice
	var created int64
	var acked *int64
	err := r.Scan(&n.Id, &n.Person, &n.Persona, &n.Stream, &n.Utterance, &n.Title, &n.Text, &n.Urgency, &n.Device, &created, &acked, &n.AckedBy)
	n.CreatedMs = uint64(created)
	if acked != nil {
		n.AckedMs = ptr(uint64(*acked))
	}
	n.Deliveries = []api.NoticeDelivery{}
	return n, err
}

// deliveries fills the deliveries of the notices.
func (db *DB) deliveries(ctx context.Context, ns []api.Notice) ([]api.Notice, error) {
	if len(ns) == 0 {
		return ns, nil
	}
	ids := make([]string, len(ns))
	at := map[string]int{}
	for i, n := range ns {
		ids[i], at[n.Id] = n.Id, i
	}
	rows, err := db.pool.Query(ctx, `SELECT notice, device, how, at_ms FROM notice_deliveries WHERE notice = ANY($1) ORDER BY at_ms, device`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var d api.NoticeDelivery
		var ms int64
		if err := rows.Scan(&id, &d.Device, &d.How, &ms); err != nil {
			return nil, err
		}
		d.AtMs = uint64(ms)
		ns[at[id]].Deliveries = append(ns[at[id]].Deliveries, d)
	}
	return ns, rows.Err()
}

func (db *DB) PutNotice(ctx context.Context, n api.Notice) (api.Notice, error) {
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO notices (`+noticeCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULL, NULL)`,
			n.Id, n.Person, n.Persona, n.Stream, n.Utterance, n.Title, n.Text, n.Urgency, n.Device, int64(n.CreatedMs)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, Channel, NoticeKey(n.Person))
		return err
	})
	n.Deliveries = []api.NoticeDelivery{}
	return n, err
}

func (db *DB) GetNotice(ctx context.Context, id string) (api.Notice, error) {
	n, err := one(scanNotice(db.pool.QueryRow(ctx, `SELECT `+noticeCols+` FROM notices WHERE id = $1`, id)))
	if err != nil {
		return n, err
	}
	ns, err := db.deliveries(ctx, []api.Notice{n})
	if err != nil {
		return n, err
	}
	return ns[0], nil
}

// PendingNotices returns the open notices of person since sinceMs for
// device (or for all devices) that the device did not get yet, oldest
// first.
func (db *DB) PendingNotices(ctx context.Context, person, device string, sinceMs uint64) ([]api.Notice, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+noticeCols+` FROM notices n
WHERE person = $1 AND acked_ms IS NULL AND created_ms >= $3 AND (device IS NULL OR device = $2)
  AND NOT EXISTS (SELECT 1 FROM notice_deliveries d WHERE d.notice = n.id AND d.device = $2)
ORDER BY created_ms, id LIMIT 50`, person, device, int64(sinceMs))
	ns, err := many(rows, err, scanNotice)
	if err != nil {
		return nil, err
	}
	return db.deliveries(ctx, ns)
}

// ListNotices returns notices of person, newest first.
func (db *DB) ListNotices(ctx context.Context, person string, open bool, limit int) ([]api.Notice, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+noticeCols+` FROM notices WHERE person = $1 AND (NOT $2 OR acked_ms IS NULL)
ORDER BY created_ms DESC, id LIMIT $3`, person, open, limit)
	ns, err := many(rows, err, scanNotice)
	if err != nil {
		return nil, err
	}
	return db.deliveries(ctx, ns)
}

// DeliverNotice records that device got the notice. A second delivery to
// the same device keeps the first.
func (db *DB) DeliverNotice(ctx context.Context, id, device, how string, ms uint64) error {
	_, err := db.pool.Exec(ctx, `INSERT INTO notice_deliveries (notice, device, how, at_ms) VALUES ($1, $2, $3, $4)
ON CONFLICT (notice, device) DO NOTHING`, id, device, how, int64(ms))
	return err
}

// AckNotice closes a notice and wakes the devices of the person, so that
// they remove it.
func (db *DB) AckNotice(ctx context.Context, n api.Notice, by string, ms uint64) error {
	return pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE notices SET acked_ms = $2, acked_by = $3 WHERE id = $1 AND acked_ms IS NULL`, n.Id, int64(ms), by); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, Channel, NoticeKey(n.Person))
		return err
	})
}

// ClosedNotices returns the ids of notices of person that device got and
// that were acknowledged after ms.
func (db *DB) ClosedNotices(ctx context.Context, person, device string, ms uint64) ([]string, error) {
	rows, err := db.pool.Query(ctx, `SELECT n.id FROM notices n JOIN notice_deliveries d ON d.notice = n.id AND d.device = $2
WHERE n.person = $1 AND n.acked_ms > $3 ORDER BY n.acked_ms LIMIT 100`, person, device, int64(ms))
	return many(rows, err, func(r pgx.Row) (string, error) {
		var id string
		return id, r.Scan(&id)
	})
}

func ptr[T any](v T) *T { return &v }
