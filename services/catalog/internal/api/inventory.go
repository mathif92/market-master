package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/xuri/excelize/v2"

	"market-master/pkg/httpx"
	"market-master/pkg/idempotency"
	"market-master/pkg/idgen"
	"market-master/pkg/tenantctx"
	"market-master/services/catalog/internal/store"
)

const (
	maxUploadBytes = 10 << 20 // 10 MiB
	maxIngestRows  = 5000
	webhookBodyMax = 1 << 20 // 1 MiB
)

type ingestItemReq struct {
	SKU      string `json:"sku"`
	Quantity int64  `json:"quantity"`
}

type ingestReq struct {
	Mode  string          `json:"mode"`
	Items []ingestItemReq `json:"items"`
}

func normalizeItems(items []ingestItemReq) []store.IngestItem {
	out := make([]store.IngestItem, 0, len(items))
	for i, it := range items {
		out = append(out, store.IngestItem{
			Row: i + 1, SKU: strings.TrimSpace(it.SKU), Quantity: it.Quantity,
		})
	}
	return out
}

// ingestStock applies a JSON batch (POST /v1/inventory/ingest). The
// Idempotency-Key reservation is completed inside the batch transaction,
// and the key doubles as the batch key — a retry can never double-apply.
func (s *Server) ingestStock(w http.ResponseWriter, r *http.Request) {
	var req ingestReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Mode != "set" && req.Mode != "delta" {
		httpx.Err(w, http.StatusBadRequest, "invalid_mode", "mode must be set or delta")
		return
	}
	if len(req.Items) == 0 {
		httpx.Err(w, http.StatusBadRequest, "empty_batch", "at least one item is required")
		return
	}
	if len(req.Items) > maxIngestRows {
		httpx.Err(w, http.StatusBadRequest, "too_many_rows",
			"batch exceeds "+strconv.Itoa(maxIngestRows)+" rows")
		return
	}
	resv := idempotency.From(r.Context())
	batchKey := "api:" + resv.Key()
	if resv == nil || batchKey == "api:" {
		batchKey = "api:" + idgen.New()
	}
	res, err := s.store.ApplyIngest(r.Context(), tenantctx.FromRequest(r), batchKey,
		"api", req.Mode, normalizeItems(req.Items),
		func(tx pgx.Tx, res store.IngestResult) error {
			return completeReservation(r.Context(), tx, resv, res)
		})
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// ingestWebhook accepts HMAC-signed pushes from external systems
// (POST /v1/inventory/webhook). The market comes from the signed payload,
// so it is registered outside the tenant middleware. A valid signature
// always yields 200 with a report; row problems live in the report.
func (s *Server) ingestWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, webhookBodyMax+1))
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "bad_body", "cannot read body")
		return
	}
	if len(body) > webhookBodyMax {
		httpx.Err(w, http.StatusRequestEntityTooLarge, "body_too_large", "max 1 MiB")
		return
	}
	sig := r.Header.Get("X-Ingest-Signature")
	if sig == "" || !verifyIngestSignature(s.ingestSecret, sig, body) {
		httpx.Err(w, http.StatusUnauthorized, "bad_signature", "invalid ingest signature")
		return
	}
	var req struct {
		EventID  string          `json:"event_id"`
		TenantID string          `json:"tenant_id"`
		Mode     string          `json:"mode"`
		Items    []ingestItemReq `json:"items"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_event", "malformed JSON payload")
		return
	}
	if req.EventID == "" || !looksLikeUUID(req.TenantID) {
		httpx.Err(w, http.StatusBadRequest, "invalid_event",
			"event_id and a valid tenant_id are required")
		return
	}
	if req.Mode != "set" && req.Mode != "delta" {
		httpx.Err(w, http.StatusBadRequest, "invalid_mode", "mode must be set or delta")
		return
	}
	if len(req.Items) == 0 || len(req.Items) > maxIngestRows {
		httpx.Err(w, http.StatusBadRequest, "invalid_event",
			"items must contain 1 to "+strconv.Itoa(maxIngestRows)+" rows")
		return
	}
	res, err := s.store.ApplyIngest(r.Context(), req.TenantID, "webhook:"+req.EventID,
		"webhook", req.Mode, normalizeItems(req.Items), nil)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	s.log.Info("ingest webhook applied", "event_id", req.EventID,
		"tenant_id", req.TenantID, "applied", res.AppliedRows,
		"replayed", res.Replayed, "errors", res.ErrorRows)
	httpx.JSON(w, http.StatusOK, res)
}

// uploadStock ingests a CSV or XLSX file (multipart field "file", optional
// form field "mode"). Dedupe is by content hash: the same bytes can never
// apply twice, which makes file-level retries safe for delta mode.
func (s *Server) uploadStock(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		httpx.Err(w, http.StatusBadRequest, "bad_multipart", err.Error())
		return
	}
	mode := r.FormValue("mode")
	if mode == "" {
		mode = "set"
	}
	if mode != "set" && mode != "delta" {
		httpx.Err(w, http.StatusBadRequest, "invalid_mode", "mode must be set or delta")
		return
	}
	_, fh, err := r.FormFile("file")
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "file_required", "multipart field file is required")
		return
	}
	if fh.Size > maxUploadBytes {
		httpx.Err(w, http.StatusRequestEntityTooLarge, "file_too_large", "max 10 MiB")
		return
	}
	f, err := fh.Open()
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "bad_file", err.Error())
		return
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxUploadBytes+1))
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "bad_file", err.Error())
		return
	}
	if len(data) > maxUploadBytes {
		httpx.Err(w, http.StatusRequestEntityTooLarge, "file_too_large", "max 10 MiB")
		return
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	var rows [][]string
	switch ext {
	case ".csv":
		rows, err = readStockCSV(data)
	case ".xlsx":
		rows, err = readStockXLSX(data)
	default:
		httpx.Err(w, http.StatusBadRequest, "unsupported_file",
			"only .csv and .xlsx are supported")
		return
	}
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_file", err.Error())
		return
	}
	items, err := stockRowsToItems(rows)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_file", err.Error())
		return
	}
	sum := sha256.Sum256(data)
	batchKey := "upload:" + hex.EncodeToString(sum[:])
	res, err := s.store.ApplyIngest(r.Context(), tenantctx.FromRequest(r), batchKey,
		"upload", mode, items, nil)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// listStockLevels returns all products with their stock state (staff view).
func (s *Server) listStockLevels(w http.ResponseWriter, r *http.Request) {
	levels, err := s.store.ListStockLevels(r.Context(), tenantctx.FromRequest(r))
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"levels": levels})
}

func (s *Server) listStockMovements(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	moves, err := s.store.ListStockMovements(r.Context(), tenantctx.FromRequest(r),
		store.MovementFilter{ProductID: r.URL.Query().Get("product_id"), Limit: limit})
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"movements": moves})
}

// patchLevel edits the low-stock threshold; for an untracked product it
// starts tracking at on_hand 0 ({"low_stock_threshold": 10} or any value).
func (s *Server) patchLevel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LowStockThreshold *int `json:"low_stock_threshold"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.LowStockThreshold == nil || *req.LowStockThreshold < 0 {
		httpx.Err(w, http.StatusBadRequest, "invalid_threshold",
			"low_stock_threshold (>= 0) is required")
		return
	}
	err := s.store.StartTracking(r.Context(), tenantctx.FromRequest(r),
		chi.URLParam(r, "product_id"), *req.LowStockThreshold)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	lv, err := s.store.GetStockLevel(r.Context(), tenantctx.FromRequest(r),
		chi.URLParam(r, "product_id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, lv)
}

// --- helpers ---

// completeReservation records the replayable response inside the batch tx.
func completeReservation(ctx context.Context, tx pgx.Tx, resv *idempotency.Reservation,
	res store.IngestResult) error {
	if resv == nil {
		return nil
	}
	body, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return resv.CompleteInTx(ctx, tx, http.StatusOK, body)
}

// verifyIngestSignature checks an HMAC-SHA256 hex signature over the body.
func verifyIngestSignature(secret, signature string, body []byte) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(signature)), []byte(expected))
}

// looksLikeUUID performs a cheap syntactic check so a garbage tenant id
// can never reach current_tenant()'s ::uuid cast.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return false
			}
		}
	}
	return true
}

// readStockCSV parses a headered CSV: columns sku|product_id and quantity.
// Format problems fail the whole file with a line number.
func readStockCSV(data []byte) ([][]string, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1 // ragged rows handled by header mapping
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) < 2 {
		return nil, errors.New("file must contain a header row and at least one data row")
	}
	return records, nil
}

func readStockXLSX(data []byte) ([][]string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, errors.New("workbook has no sheets")
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, errors.New("sheet must contain a header row and at least one data row")
	}
	return rows, nil
}

// stockRowsToItems maps headered rows to ingest items (row 1 = header).
func stockRowsToItems(rows [][]string) ([]store.IngestItem, error) {
	if len(rows) < 2 {
		return nil, errors.New("file must contain a header row and at least one data row")
	}
	header := make([]string, len(rows[0]))
	for i, h := range rows[0] {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		header[i] = h
	}
	skuCol, qtyCol := -1, -1
	for i, h := range header {
		switch h {
		case "sku", "product_id", "product":
			skuCol = i
		case "quantity", "qty", "on_hand":
			qtyCol = i
		}
	}
	if skuCol < 0 || qtyCol < 0 {
		return nil, errors.New("header must contain sku (or product_id) and quantity columns")
	}
	out := make([]store.IngestItem, 0, len(rows)-1)
	for i, rec := range rows[1:] {
		row := i + 2
		if skuCol >= len(rec) || qtyCol >= len(rec) {
			return nil, errors.New("row " + strconv.Itoa(row) + ": missing cells")
		}
		sku := strings.TrimSpace(rec[skuCol])
		if sku == "" {
			return nil, errors.New("row " + strconv.Itoa(row) + ": sku is empty")
		}
		q := strings.TrimSpace(rec[qtyCol])
		if q == "" {
			return nil, errors.New("row " + strconv.Itoa(row) + ": quantity is empty")
		}
		qty, err := strconv.ParseInt(q, 10, 64)
		if err != nil {
			return nil, errors.New("row " + strconv.Itoa(row) + ": quantity must be an integer")
		}
		out = append(out, store.IngestItem{Row: row, SKU: sku, Quantity: qty})
	}
	if len(out) > maxIngestRows {
		return nil, errors.New("file exceeds " + strconv.Itoa(maxIngestRows) + " rows")
	}
	return out, nil
}
