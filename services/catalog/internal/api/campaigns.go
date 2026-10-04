package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"market-master/pkg/httpx"
	"market-master/pkg/idempotency"
	"market-master/pkg/tenantctx"
	"market-master/services/catalog/internal/store"
)

type campaignReq struct {
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	RuleType       string    `json:"rule_type"`
	RuleValue      int       `json:"rule_value"`
	ScopeType      string    `json:"scope_type"`
	ScopeID        string    `json:"scope_id"`
	RequiresCode   bool      `json:"requires_code"`
	StartsAt       time.Time `json:"starts_at"`
	EndsAt         time.Time `json:"ends_at"`
	MaxRedemptions *int      `json:"max_redemptions"`
}

func (req campaignReq) toStore() store.Campaign {
	return store.Campaign{
		Name:           req.Name,
		Status:         req.Status,
		RuleType:       req.RuleType,
		RuleValue:      req.RuleValue,
		ScopeType:      req.ScopeType,
		ScopeID:        req.ScopeID,
		RequiresCode:   req.RequiresCode,
		StartsAt:       req.StartsAt,
		EndsAt:         req.EndsAt,
		MaxRedemptions: req.MaxRedemptions,
	}
}

// listActiveCampaigns is the public banner feed: live, non-code campaigns
// for the market resolved from the host (tenant required, no JWT).
func (s *Server) listActiveCampaigns(w http.ResponseWriter, r *http.Request) {
	campaigns, err := s.store.ListActiveCampaigns(r.Context(), tenantctx.FromRequest(r))
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"campaigns": campaigns})
}

func (s *Server) listCampaigns(w http.ResponseWriter, r *http.Request) {
	campaigns, err := s.store.ListCampaigns(r.Context(), tenantctx.FromRequest(r))
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"campaigns": campaigns})
}

func (s *Server) getCampaign(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetCampaign(r.Context(), tenantctx.FromRequest(r), chi.URLParam(r, "id"))
	if err != nil {
		writeCampaignErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

// createCampaign is NOT naturally idempotent: it requires an
// Idempotency-Key and completes the reservation inside the insert tx.
func (s *Server) createCampaign(w http.ResponseWriter, r *http.Request) {
	var req campaignReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	resv := idempotency.From(r.Context())
	c, err := s.store.CreateCampaign(r.Context(), tenantctx.FromRequest(r), req.toStore(),
		func(tx pgx.Tx, created store.Campaign) error {
			body, err := json.Marshal(created)
			if err != nil {
				return err
			}
			return resv.CompleteInTx(r.Context(), tx, http.StatusCreated, body)
		})
	if err != nil {
		writeCampaignErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, c)
}

// updateCampaign is a full PUT (naturally idempotent — same body, same
// final state — so no idempotency key is required). Status must be sent
// explicitly so an omitted field can never silently deactivate a campaign.
func (s *Server) updateCampaign(w http.ResponseWriter, r *http.Request) {
	var req campaignReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Status == "" {
		httpx.Err(w, http.StatusBadRequest, "invalid_campaign", "status is required (draft, active or archived)")
		return
	}
	c, err := s.store.UpdateCampaign(r.Context(), tenantctx.FromRequest(r),
		chi.URLParam(r, "id"), req.toStore())
	if err != nil {
		writeCampaignErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

// archiveCampaign soft-deletes; idempotent.
func (s *Server) archiveCampaign(w http.ResponseWriter, r *http.Request) {
	err := s.store.ArchiveCampaign(r.Context(), tenantctx.FromRequest(r), chi.URLParam(r, "id"))
	if err != nil {
		writeCampaignErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type codesReq struct {
	Count   int `json:"count"`
	MaxUses int `json:"max_uses"`
}

// generateCodes mints fresh random codes — not naturally idempotent, so an
// Idempotency-Key is required and completed inside the same tx.
func (s *Server) generateCodes(w http.ResponseWriter, r *http.Request) {
	var req codesReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Count == 0 {
		req.Count = 1
	}
	resv := idempotency.From(r.Context())
	codes, err := s.store.GenerateCodes(r.Context(), tenantctx.FromRequest(r),
		chi.URLParam(r, "id"), req.Count, req.MaxUses,
		func(tx pgx.Tx, minted []store.CampaignCode) error {
			body, err := json.Marshal(map[string]any{"codes": minted})
			if err != nil {
				return err
			}
			return resv.CompleteInTx(r.Context(), tx, http.StatusCreated, body)
		})
	if err != nil {
		writeCampaignErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"codes": codes})
}

func (s *Server) listCodes(w http.ResponseWriter, r *http.Request) {
	codes, err := s.store.ListCodes(r.Context(), tenantctx.FromRequest(r), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"codes": codes})
}

func (s *Server) deleteCode(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteCode(r.Context(), tenantctx.FromRequest(r), chi.URLParam(r, "code_id"))
	if err != nil {
		writeCampaignErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeCampaignErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Err(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, store.ErrInvalidCampaign):
		httpx.Err(w, http.StatusBadRequest, "invalid_campaign",
			strings.TrimPrefix(err.Error(), store.ErrInvalidCampaign.Error()+": "))
	case errors.Is(err, store.ErrCodeInUse):
		httpx.Err(w, http.StatusConflict, "code_in_use",
			"code has redemptions and is kept for audit")
	default:
		httpx.Internal(w, err)
	}
}
