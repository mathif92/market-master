package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrConflict    = errors.New("idempotency key reused with different request payload")
	ErrInProgress  = errors.New("request with this idempotency key is still in progress")
	ErrKeyRequired = errors.New("Idempotency-Key header is required")
)

const schema = `
CREATE TABLE IF NOT EXISTS idempotency_keys (
	scope       TEXT NOT NULL,
	key         TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	status      TEXT NOT NULL DEFAULT 'in_progress',
	response_status INT,
	response_body BYTEA,
	locked_until TIMESTAMPTZ NOT NULL,
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (scope, key)
);
`

func EnsureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schema)
	return err
}

// Store is the reservation/replay store for HTTP idempotency keys.
type Store struct {
	pool  *pgxpool.Pool
	lease time.Duration
}

func NewStore(pool *pgxpool.Pool, lease time.Duration) *Store {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	return &Store{pool: pool, lease: lease}
}

type Result struct {
	Outcome        Outcome
	ResponseStatus int
	ResponseBody   []byte
}

type Outcome int

const (
	// Reserved means the caller now owns this key and must execute the request.
	Reserved Outcome = iota
	// Replay means a completed response for the same key+payload exists.
	Replay
	// InProgress means another attempt with the same key+payload is running.
	InProgress
	// Conflict means the key was used with a different payload.
	Conflict
)

// Reserve attempts to claim (scope, key) for a request with the given body
// hash. Completed responses are replayed; in-flight claims are leased with
// an expiry so a crashed worker's key becomes claimable again.
func (s *Store) Reserve(ctx context.Context, scope, key, reqHash string) (Result, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		storedHash, status string
		respStatus         *int
		respBody           []byte
		lockedUntil        time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT request_hash, status, response_status, response_body, locked_until
		FROM idempotency_keys WHERE scope = $1 AND key = $2
		FOR UPDATE`, scope, key).
		Scan(&storedHash, &status, &respStatus, &respBody, &lockedUntil)
	claim := false
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_, err = tx.Exec(ctx, `
			INSERT INTO idempotency_keys (scope, key, request_hash, locked_until)
			VALUES ($1, $2, $3, now() + make_interval(secs => $4))`,
			scope, key, reqHash, s.lease.Seconds())
		if err != nil {
			// Concurrent insert of the same PK: fall through as contention.
			if commitErr := tx.Commit(ctx); commitErr == nil {
				return Result{Outcome: InProgress}, nil
			}
			return Result{}, err
		}
		claim = true
	case err != nil:
		return Result{}, err
	default:
		if storedHash != reqHash {
			return Result{Outcome: Conflict}, nil
		}
		switch status {
		case "completed":
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return Result{}, commitErr
			}
			body := respBody
			rs := 0
			if respStatus != nil {
				rs = *respStatus
			}
			return Result{Outcome: Replay, ResponseStatus: rs, ResponseBody: body}, nil
		default: // in_progress
			if time.Now().Before(lockedUntil) {
				return Result{Outcome: InProgress}, nil
			}
			tag, err := tx.Exec(ctx, `
				UPDATE idempotency_keys SET locked_until = now() + make_interval(secs => $3)
				WHERE scope = $1 AND key = $2 AND status = 'in_progress' AND locked_until <= now()`,
				scope, key, s.lease.Seconds())
			if err != nil {
				return Result{}, err
			}
			claim = tag.RowsAffected() == 1
		}
	}
	if !claim {
		return Result{Outcome: InProgress}, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return Result{Outcome: Reserved}, nil
}

// Complete stores the response for future replays.
func (s *Store) Complete(ctx context.Context, scope, key string, status int, body []byte) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE idempotency_keys
		SET status = 'completed', response_status = $3, response_body = $4
		WHERE scope = $1 AND key = $2`, scope, key, status, body)
	return err
}

// Release frees a reserved key after a failed attempt so it can be retried.
func (s *Store) Release(ctx context.Context, scope, key string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM idempotency_keys WHERE scope = $1 AND key = $2 AND status = 'in_progress'`,
		scope, key)
	return err
}

type resKey struct{}

// Reservation is a claimed idempotency key placed on the request context.
// Handlers that write to the database SHOULD complete it inside the same
// transaction as their business writes (CompleteInTx), so that "response
// recorded" and "state changed" commit atomically — a crash in between can
// then never produce a duplicate execution on retry.
type Reservation struct {
	store *Store
	scope string
	key   string
	done  bool
}

func withReservation(ctx context.Context, r *Reservation) context.Context {
	return context.WithValue(ctx, resKey{}, r)
}

// From extracts the reservation, or nil when the request had no key.
func From(ctx context.Context) *Reservation {
	r, _ := ctx.Value(resKey{}).(*Reservation)
	return r
}

// CompleteInTx records the replayable response inside the caller's
// transaction.
func (r *Reservation) CompleteInTx(ctx context.Context, tx pgx.Tx, status int, body []byte) error {
	if r == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE idempotency_keys
		SET status = 'completed', response_status = $3, response_body = $4
		WHERE scope = $1 AND key = $2 AND status = 'in_progress'`,
		r.scope, r.key, status, body)
	if err != nil {
		return err
	}
	r.done = true
	return nil
}

func (r *Reservation) completed() bool { return r != nil && r.done }

// Hash returns the canonical request hash (method + path + body).
func Hash(method, path string, body []byte) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\n%s\n", method, path)
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// Middleware enforces the Idempotency-Key contract on mutating requests.
// Reserved requests capture the handler's response; successful responses
// (2xx/4xx) are replayed for later retries, 5xx release the key so the
// client can safely retry.
func (s *Store) Middleware(scope string, required bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			default:
				next.ServeHTTP(w, r)
				return
			}
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				if required {
					writeErr(w, http.StatusBadRequest, ErrKeyRequired.Error())
				} else {
					next.ServeHTTP(w, r)
				}
				return
			}
			body, err := readBody(r)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid request body")
				return
			}
			hash := Hash(r.Method, r.URL.Path, body)
			res, err := s.Reserve(r.Context(), scope, key, hash)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "idempotency store unavailable")
				return
			}
			switch res.Outcome {
			case Replay:
				w.Header().Set("Idempotency-Replayed", "true")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(res.ResponseStatus)
				_, _ = w.Write(res.ResponseBody)
				return
			case Conflict:
				writeErr(w, http.StatusConflict, ErrConflict.Error())
				return
			case InProgress:
				w.Header().Set("Retry-After", "1")
				writeErr(w, http.StatusConflict, ErrInProgress.Error())
				return
			}
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			resv := &Reservation{store: s, scope: scope, key: key}
			next.ServeHTTP(rec, r.WithContext(withReservation(r.Context(), resv)))
			if resv.completed() {
				return // response committed together with the business tx
			}
			// The handler did not complete the reservation inside its own
			// transaction: release so a retry can re-execute. Storing a
			// non-committed response here could replay a result for state
			// that never landed.
			_ = s.Release(r.Context(), scope, key)
		})
	}
}

type recorder struct {
	http.ResponseWriter
	status int
	buf    []byte
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.buf = append(r.buf, b...)
	return r.ResponseWriter.Write(b)
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"code":"` + http.StatusText(status) + `","message":"` + msg + `"}}`))
}
