package store

import (
	"context"
	"errors"

	api "github.com/catalystcommunity/magi/primitives/speech/api"
	"github.com/jackc/pgx/v5"
)

// Profile returns the prints of owner for model. Without a record, the
// profile has no prints.
func (db *DB) Profile(ctx context.Context, owner, model string) (api.SpeakerProfile, error) {
	p := api.SpeakerProfile{Owner: owner, Model: model, Prints: []api.SpeakerPrint{}}
	var vectors [][]byte
	var sources []string
	var created []int64
	var updated int64
	err := db.pool.QueryRow(ctx, `SELECT vectors, sources, created, sample IS NOT NULL, updated_ms
		FROM speaker_profiles WHERE owner = $1 AND model = $2`, owner, model).Scan(&vectors, &sources, &created, &p.HasSample, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.UpdatedMs = uint64(updated)
	for i, v := range vectors {
		pr := api.SpeakerPrint{Vector: v}
		if i < len(sources) && sources[i] != "" {
			pr.Source = &sources[i]
		}
		if i < len(created) {
			pr.CreatedMs = uint64(created[i])
		}
		p.Prints = append(p.Prints, pr)
	}
	return p, nil
}

// PutProfile replaces the prints of p.Owner for p.Model. sample nil keeps
// the sample that is there.
func (db *DB) PutProfile(ctx context.Context, p api.SpeakerProfile, sample []byte, sampleType string) (api.SpeakerProfile, error) {
	vectors := make([][]byte, len(p.Prints))
	sources := make([]string, len(p.Prints))
	created := make([]int64, len(p.Prints))
	for i, pr := range p.Prints {
		vectors[i], created[i] = pr.Vector, int64(pr.CreatedMs)
		if pr.Source != nil {
			sources[i] = *pr.Source
		}
	}
	_, err := db.pool.Exec(ctx, `INSERT INTO speaker_profiles (owner, model, vectors, sources, created, sample, sample_type, updated_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (owner, model) DO UPDATE SET vectors = $3, sources = $4, created = $5,
		  sample = COALESCE($6, speaker_profiles.sample),
		  sample_type = CASE WHEN $6::bytea IS NULL THEN speaker_profiles.sample_type ELSE $7 END, updated_ms = $8`,
		p.Owner, p.Model, vectors, sources, created, sample, sampleType, int64(p.UpdatedMs))
	if err != nil {
		return api.SpeakerProfile{}, err
	}
	return db.Profile(ctx, p.Owner, p.Model)
}

// Sample returns the speech of owner that the extraction uses, with its
// media type. It is nil when there is none.
func (db *DB) Sample(ctx context.Context, owner, model string) ([]byte, string, error) {
	var sample []byte
	var mt string
	err := db.pool.QueryRow(ctx, `SELECT sample, sample_type FROM speaker_profiles WHERE owner = $1 AND model = $2`, owner, model).Scan(&sample, &mt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	return sample, mt, err
}

// DeleteProfiles removes the profile of owner for model, or for all
// models when model is "". It returns the count of profiles removed.
func (db *DB) DeleteProfiles(ctx context.Context, owner, model string) (uint64, error) {
	t, err := db.pool.Exec(ctx, `DELETE FROM speaker_profiles WHERE owner = $1 AND ($2 = '' OR model = $2)`, owner, model)
	return uint64(t.RowsAffected()), err
}
