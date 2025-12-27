package db

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct{ pool *pgxpool.Pool }

type User struct {
	ID, Username, Password string
	Rating                 int
}

type Text struct {
	ID, Length int
	Content    string
}

type MatchResult struct {
	UserID    string
	WPM, Rank int
	Accuracy  float64
}

func New(url string, maxConns int32) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = maxConns
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	return &DB{pool: p}, err
}

func (d *DB) Close() { d.pool.Close() }

func (d *DB) CreateUser(ctx context.Context, name, hash string) (string, error) {
	var id string
	err := d.pool.QueryRow(ctx, "INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id", name, hash).Scan(&id)
	return id, err
}

func (d *DB) GetLeaderboard(ctx context.Context, limit int) ([]map[string]any, error) {
    rows, err := d.pool.Query(ctx, "SELECT username, rating FROM users ORDER BY rating ASC LIMIT $1", limit)
    if err != nil {
        return nil, err
    }
    return pgx.CollectRows(rows, pgx.RowToMap)
}

func (d *DB) GetUser(ctx context.Context, name string) (*User, error) {
	rows, err := d.pool.Query(ctx, "SELECT id, username, password_hash as password, rating FROM users WHERE username=$1", name)
	if err != nil {
		return nil, err
	}
	u, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[User])
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *DB) GetUserByID(ctx context.Context, id string) (*User, error) {
	rows, err := d.pool.Query(ctx, "SELECT id, username, password_hash as password, rating FROM users WHERE id=$1", id)
	if err != nil {
		return nil, err
	}
	u, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[User])
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *DB) UpdateRating(ctx context.Context, uid string, change int) error {
	_, err := d.pool.Exec(ctx, "UPDATE users SET rating = rating + $1 WHERE id = $2", change, uid)
	return err
}

func (d *DB) GetText(ctx context.Context, lang, cat string, id int) (*Text, error) {
	q, args := "SELECT id, content, length FROM texts WHERE id=$1", []any{id}
	if id == 0 {
		q, args = "SELECT id, content, length FROM texts WHERE language=$1 AND category=$2 ORDER BY RANDOM() LIMIT 1", []any{lang, cat}
	}
	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	t, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[Text])
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (d *DB) GenerateText(ctx context.Context, lang string) (*Text, error) {
	rows, err := d.pool.Query(ctx, "SELECT content FROM words WHERE language=$1 ORDER BY RANDOM() LIMIT 20", lang)
	if err != nil {
		return d.GetText(ctx, lang, "general", 0)
	}
	words, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(words) == 0 {
		return d.GetText(ctx, lang, "general", 0)
	}
	content := strings.Join(words, " ")
	return &Text{Content: content, Length: len([]rune(content))}, nil
}

func (d *DB) SaveMatch(ctx context.Context, textID int, res []MatchResult) error {
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		var mid string
		if err := tx.QueryRow(ctx, "INSERT INTO matches (text_id, ended_at) VALUES ($1, NOW()) RETURNING id", textID).Scan(&mid); err != nil {
			return err
		}
		b := &pgx.Batch{}
		for _, r := range res {
			b.Queue("INSERT INTO match_results (match_id, user_id, wpm, accuracy, rank) VALUES ($1, $2, $3, $4, $5)", mid, r.UserID, r.WPM, r.Accuracy, r.Rank)
		}
		return tx.SendBatch(ctx, b).Close()
	})
}

func (d *DB) GetHistory(ctx context.Context, uid string, limit int, cursor string) ([]map[string]any, string, error) {
	args := []any{uid, limit}
	query := `SELECT m.id, mr.wpm, mr.accuracy, mr.rank, m.ended_at, COALESCE(left(t.content, 50), '[удалено]') as preview 
		FROM match_results mr 
		JOIN matches m ON mr.match_id = m.id 
		LEFT JOIN texts t ON m.text_id = t.id 
		WHERE mr.user_id = $1`

	if parts := strings.Split(cursor, ","); len(parts) == 2 {
		query += " AND (m.ended_at, m.id) < ($3, $4)"
		args = append(args, parts[0], parts[1])
	}
	query += " ORDER BY m.ended_at DESC, m.id DESC LIMIT $2"

	rows, err := d.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	type Row struct {
		ID, Preview string
		WPM, Rank   int
		Accuracy    float64
		EndedAt     time.Time
	}
	data, err := pgx.CollectRows(rows, pgx.RowToStructByName[Row])
	if err != nil {
		return nil, "", err
	}

	res := make([]map[string]any, len(data))
	var next string
	for i, r := range data {
		res[i] = map[string]any{"match_id": r.ID, "wpm": r.WPM, "accuracy": r.Accuracy, "rank": r.Rank, "date": r.EndedAt, "text_preview": r.Preview}
		if i == len(data)-1 {
			next = r.EndedAt.Format(time.RFC3339) + "," + r.ID
		}
	}
	return res, next, nil
}

func (d *DB) GetCategories(ctx context.Context) ([]string, error) {
    query := "SELECT DISTINCT category FROM texts"
    rows, err := d.pool.Query(ctx, query)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var categories []string
    for rows.Next() {
        var cat string
        if err := rows.Scan(&cat); err != nil {
            return nil, err
        }
        categories = append(categories, cat)
    }
    return categories, nil
}
