package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"market-master/pkg/psql"
)

// Campaign is a time-windowed discount: percent (basis points) or fixed
// cents off the unit list price, scoped sitewide/category/product, with
// optional coupon codes. status='active' AND now() inside the window makes
// it live; read-time pricing only ever surfaces non-code campaigns.
type Campaign struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Status           string    `json:"status"` // draft | active | archived
	RuleType         string    `json:"rule_type"`
	RuleValue        int       `json:"rule_value"`
	ScopeType        string    `json:"scope_type"`
	ScopeID          string    `json:"scope_id,omitempty"`
	RequiresCode     bool      `json:"requires_code"`
	StartsAt         time.Time `json:"starts_at"`
	EndsAt           time.Time `json:"ends_at"`
	MaxRedemptions   *int      `json:"max_redemptions,omitempty"`
	RedemptionsCount int       `json:"redemptions_count"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type CampaignCode struct {
	ID         string    `json:"id"`
	CampaignID string    `json:"campaign_id"`
	Code       string    `json:"code"`
	MaxUses    *int      `json:"max_uses,omitempty"`
	UsesCount  int       `json:"uses_count"`
	CreatedAt  time.Time `json:"created_at"`
}

// ProductCampaign is the read-time sale annotation embedded in products.
type ProductCampaign struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	RuleType  string    `json:"rule_type"`
	RuleValue int       `json:"rule_value"`
	EndsAt    time.Time `json:"ends_at"`
}

var (
	ErrInvalidCampaign = errors.New("invalid campaign")
	// ErrInvalidCoupon: the presented code doesn't exist / isn't live.
	ErrInvalidCoupon = errors.New("invalid coupon code")
	// ErrCampaignLimit: a guarded redemption counter refused admission.
	ErrCampaignLimit = errors.New("campaign usage limit reached")
)

// PricedLine is one order line after campaign pricing: ListPriceCents is
// the canonical catalog price, UnitFinalCents what the customer pays.
type PricedLine struct {
	ProductID      string `json:"product_id"`
	ListPriceCents int64  `json:"list_price_cents"`
	UnitFinalCents int64  `json:"unit_final_cents"`
}

type AppliedCampaign struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	RuleType      string `json:"rule_type"`
	RuleValue     int    `json:"rule_value"`
	DiscountCents int64  `json:"discount_cents"`
	CouponCode    string `json:"coupon_code,omitempty"`
}

type ReserveResult struct {
	Lines    []PricedLine
	Campaign *AppliedCampaign
}

// ComputeDiscount returns the per-unit discount in cents for a list price.
// percent: floor(list * bp / 10000); fixed: the rule value. The final unit
// price is clamped to >= 1 cent (free goods are out of scope), so the
// discount never exceeds list-1.
func ComputeDiscount(ruleType string, ruleValue int, listPriceCents int64) int64 {
	if listPriceCents <= 0 {
		return 0
	}
	var d int64
	switch ruleType {
	case "percent":
		d = listPriceCents * int64(ruleValue) / 10000
	case "fixed":
		d = int64(ruleValue)
	default:
		return 0
	}
	if d > listPriceCents-1 {
		d = listPriceCents - 1
	}
	if d < 0 {
		d = 0
	}
	return d
}

// specificity ranks scopes for tie-breaks: product > category > sitewide.
func specificity(scopeType string) int {
	switch scopeType {
	case "product":
		return 2
	case "category":
		return 1
	default:
		return 0
	}
}

func scopeCovers(c Campaign, productID, categoryID string) bool {
	switch c.ScopeType {
	case "sitewide":
		return true
	case "category":
		return c.ScopeID != "" && c.ScopeID == categoryID
	case "product":
		return c.ScopeID != "" && c.ScopeID == productID
	default:
		return false
	}
}

// bestCampaignFor picks the campaign giving the greatest per-unit discount
// for one product; ties go to the more specific scope, then lower id.
// Order-level selection uses the same rule, so read-time badges always
// match what checkout charges.
func bestCampaignFor(campaigns []Campaign, productID, categoryID string, listPriceCents int64) (Campaign, bool) {
	var best Campaign
	var bestDisc int64
	found := false
	for _, c := range campaigns {
		if !scopeCovers(c, productID, categoryID) {
			continue
		}
		d := ComputeDiscount(c.RuleType, c.RuleValue, listPriceCents)
		switch {
		case !found,
			d > bestDisc,
			d == bestDisc && specificity(c.ScopeType) > specificity(best.ScopeType),
			d == bestDisc && specificity(c.ScopeType) == specificity(best.ScopeType) && c.ID < best.ID:
			best, bestDisc, found = c, d, true
		}
	}
	return best, found
}

func validateCampaign(c *Campaign) error {
	c.Name = strings.TrimSpace(c.Name)
	c.ScopeID = strings.TrimSpace(c.ScopeID)
	if c.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidCampaign)
	}
	switch c.Status {
	case "":
		c.Status = "draft"
	case "draft", "active", "archived":
	default:
		return fmt.Errorf("%w: status must be draft, active or archived", ErrInvalidCampaign)
	}
	switch c.RuleType {
	case "percent":
		if c.RuleValue < 1 || c.RuleValue > 10000 {
			return fmt.Errorf("%w: percent rule_value is basis points (1..10000)", ErrInvalidCampaign)
		}
	case "fixed":
		if c.RuleValue < 1 {
			return fmt.Errorf("%w: fixed rule_value must be positive cents", ErrInvalidCampaign)
		}
	default:
		return fmt.Errorf("%w: rule_type must be percent or fixed", ErrInvalidCampaign)
	}
	switch c.ScopeType {
	case "sitewide":
		c.ScopeID = ""
	case "category", "product":
		if c.ScopeID == "" {
			return fmt.Errorf("%w: scope_id is required for %s scope", ErrInvalidCampaign, c.ScopeType)
		}
	default:
		return fmt.Errorf("%w: scope_type must be sitewide, category or product", ErrInvalidCampaign)
	}
	if !c.EndsAt.After(c.StartsAt) {
		return fmt.Errorf("%w: ends_at must be after starts_at", ErrInvalidCampaign)
	}
	if c.MaxRedemptions != nil && *c.MaxRedemptions < 1 {
		return fmt.Errorf("%w: max_redemptions must be positive", ErrInvalidCampaign)
	}
	return nil
}

const campaignCols = `id, name, status, rule_type, rule_value, scope_type,
	COALESCE(scope_id::text, ''), requires_code, starts_at, ends_at,
	max_redemptions, redemptions_count, created_at, updated_at`

func scanCampaign(row pgx.Row) (Campaign, error) {
	var c Campaign
	err := row.Scan(&c.ID, &c.Name, &c.Status, &c.RuleType, &c.RuleValue,
		&c.ScopeType, &c.ScopeID, &c.RequiresCode, &c.StartsAt, &c.EndsAt,
		&c.MaxRedemptions, &c.RedemptionsCount, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// checkScopeTarget verifies the category/product a scope points at exists.
func checkScopeTarget(ctx context.Context, tx pgx.Tx, tenantID string, c Campaign) error {
	var exists bool
	var err error
	switch c.ScopeType {
	case "category":
		err = tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM categories WHERE tenant_id = $1 AND id = $2)`,
			tenantID, c.ScopeID).Scan(&exists)
	case "product":
		err = tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM products WHERE tenant_id = $1 AND id = $2)`,
			tenantID, c.ScopeID).Scan(&exists)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: scope target not found", ErrInvalidCampaign)
	}
	return nil
}

// CreateCampaign validates + inserts a campaign. beforeCommit (optional)
// runs in the same tx — used to complete an idempotency reservation.
func (s *Store) CreateCampaign(ctx context.Context, tenantID string, c Campaign,
	beforeCommit func(tx pgx.Tx, c Campaign) error) (Campaign, error) {
	if err := validateCampaign(&c); err != nil {
		return Campaign{}, err
	}
	var out Campaign
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := checkScopeTarget(ctx, tx, tenantID, c); err != nil {
			return err
		}
		created, err := scanCampaign(tx.QueryRow(ctx, `
			INSERT INTO campaigns (tenant_id, name, status, rule_type, rule_value,
				scope_type, scope_id, requires_code, starts_at, ends_at, max_redemptions)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid, $8, $9, $10, $11)
			RETURNING `+campaignCols,
			tenantID, c.Name, c.Status, c.RuleType, c.RuleValue, c.ScopeType,
			c.ScopeID, c.RequiresCode, c.StartsAt, c.EndsAt, c.MaxRedemptions))
		if err != nil {
			return err
		}
		out = created
		if beforeCommit != nil {
			return beforeCommit(tx, created)
		}
		return nil
	})
	if err != nil {
		return Campaign{}, err
	}
	return out, nil
}

func (s *Store) ListCampaigns(ctx context.Context, tenantID string) ([]Campaign, error) {
	out := []Campaign{}
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT `+campaignCols+`
			FROM campaigns WHERE tenant_id = $1
			ORDER BY starts_at DESC, created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCampaign(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) GetCampaign(ctx context.Context, tenantID, id string) (Campaign, error) {
	var c Campaign
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		c, err = scanCampaign(tx.QueryRow(ctx, `
			SELECT `+campaignCols+` FROM campaigns
			WHERE tenant_id = $1 AND id = $2`, tenantID, id))
		return err
	})
	return c, mapErr(err)
}

// UpdateCampaign replaces a campaign wholesale (admin form semantics);
// MaxRedemptions=nil clears the cap.
func (s *Store) UpdateCampaign(ctx context.Context, tenantID, id string, c Campaign) (Campaign, error) {
	if err := validateCampaign(&c); err != nil {
		return Campaign{}, err
	}
	var out Campaign
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := checkScopeTarget(ctx, tx, tenantID, c); err != nil {
			return err
		}
		updated, err := scanCampaign(tx.QueryRow(ctx, `
			UPDATE campaigns SET
				name = $3, status = $4, rule_type = $5, rule_value = $6,
				scope_type = $7, scope_id = NULLIF($8, '')::uuid,
				requires_code = $9, starts_at = $10, ends_at = $11,
				max_redemptions = $12, updated_at = now()
			WHERE tenant_id = $1 AND id = $2
			RETURNING `+campaignCols,
			tenantID, id, c.Name, c.Status, c.RuleType, c.RuleValue,
			c.ScopeType, c.ScopeID, c.RequiresCode, c.StartsAt, c.EndsAt,
			c.MaxRedemptions))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = updated
		return nil
	})
	if err != nil {
		return Campaign{}, err
	}
	return out, nil
}

// ArchiveCampaign soft-deletes (DELETE keeps the redemption ledger intact).
// Idempotent: archiving an archived campaign is a no-op.
func (s *Store) ArchiveCampaign(ctx context.Context, tenantID, id string) error {
	return psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE campaigns SET status = 'archived', updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND status <> 'archived'`,
			tenantID, id)
		return err
	})
}

var codeAlphabet = []byte("ABCDEFGHJKLMNPQRSTUVWXYZ23456789")

func randomCode(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil { // crypto/rand: cannot fail in practice
		panic(err)
	}
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b)
}

// GenerateCodes mints count coupon codes for a campaign (uppercase,
// 10 chars, crypto-random). Retries on the astronomically unlikely
// UNIQUE collision. beforeCommit runs in the same tx.
func (s *Store) GenerateCodes(ctx context.Context, tenantID, campaignID string,
	count, maxUses int, beforeCommit func(tx pgx.Tx, codes []CampaignCode) error) ([]CampaignCode, error) {
	if count < 1 || count > 100 {
		return nil, fmt.Errorf("%w: count must be 1..100", ErrInvalidCampaign)
	}
	if maxUses < 0 {
		return nil, fmt.Errorf("%w: max_uses must be >= 0", ErrInvalidCampaign)
	}
	out := []CampaignCode{}
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var active bool
		if err := tx.QueryRow(ctx,
			`SELECT status = 'active' FROM campaigns WHERE tenant_id = $1 AND id = $2`,
			tenantID, campaignID).Scan(&active); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		for i := 0; i < count; i++ {
			var cc CampaignCode
			var err error
			for attempt := 0; attempt < 5; attempt++ {
				var max *int
				if maxUses > 0 {
					max = &maxUses
				}
				err = tx.QueryRow(ctx, `
					INSERT INTO campaign_codes (tenant_id, campaign_id, code, max_uses)
					VALUES ($1, $2, $3, $4)
					RETURNING id, campaign_id, code, max_uses, uses_count, created_at`,
					tenantID, campaignID, randomCode(10), max).
					Scan(&cc.ID, &cc.CampaignID, &cc.Code, &cc.MaxUses,
						&cc.UsesCount, &cc.CreatedAt)
				if err == nil {
					break
				}
				if !isUniqueViolation(err) || attempt == 4 {
					return err
				}
			}
			out = append(out, cc)
		}
		if beforeCommit != nil {
			return beforeCommit(tx, out)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *Store) ListCodes(ctx context.Context, tenantID, campaignID string) ([]CampaignCode, error) {
	out := []CampaignCode{}
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, campaign_id, code, max_uses, uses_count, created_at
			FROM campaign_codes
			WHERE tenant_id = $1 AND campaign_id = $2
			ORDER BY created_at DESC`, tenantID, campaignID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var cc CampaignCode
			if err := rows.Scan(&cc.ID, &cc.CampaignID, &cc.Code, &cc.MaxUses,
				&cc.UsesCount, &cc.CreatedAt); err != nil {
				return err
			}
			out = append(out, cc)
		}
		return rows.Err()
	})
	return out, err
}

// DeleteCode removes an unused code; used codes are refused (409).
func (s *Store) DeleteCode(ctx context.Context, tenantID, codeID string) error {
	return psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var uses int
		err := tx.QueryRow(ctx,
			`SELECT uses_count FROM campaign_codes WHERE tenant_id = $1 AND id = $2`,
			tenantID, codeID).Scan(&uses)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if uses > 0 {
			return ErrCodeInUse
		}
		_, err = tx.Exec(ctx,
			`DELETE FROM campaign_codes WHERE tenant_id = $1 AND id = $2`,
			tenantID, codeID)
		return err
	})
}

// ErrCodeInUse: codes with redemptions are kept for audit.
var ErrCodeInUse = errors.New("code has been used")

// loadLiveCampaigns returns campaigns that are active and inside their
// window right now. includeCode=false restricts to auto-applied ones
// (what read-time prices may surface).
func loadLiveCampaigns(ctx context.Context, tx pgx.Tx, tenantID string, includeCode bool) ([]Campaign, error) {
	out := []Campaign{}
	rows, err := tx.Query(ctx, `
		SELECT `+campaignCols+`
		FROM campaigns
		WHERE tenant_id = $1 AND status = 'active'
			AND now() >= starts_at AND now() < ends_at
			AND ($2 OR NOT requires_code)
		ORDER BY created_at`, tenantID, includeCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListActiveCampaigns is the public banner feed: live auto campaigns only.
func (s *Store) ListActiveCampaigns(ctx context.Context, tenantID string) ([]Campaign, error) {
	var out []Campaign
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = loadLiveCampaigns(ctx, tx, tenantID, false)
		return err
	})
	if out == nil {
		out = []Campaign{}
	}
	return out, err
}

// lookupCoupon resolves a presented code to its live campaign.
// The code/campaign pair must be active and inside the window now.
func lookupCoupon(ctx context.Context, tx pgx.Tx, tenantID, code string) (CampaignCode, Campaign, error) {
	var cc CampaignCode
	var c Campaign
	err := tx.QueryRow(ctx, `
		SELECT cc.id, cc.campaign_id, cc.code, cc.max_uses, cc.uses_count,
		       c.id, c.name, c.status, c.rule_type, c.rule_value, c.scope_type,
		       COALESCE(c.scope_id::text, ''), c.requires_code, c.starts_at,
		       c.ends_at, c.max_redemptions, c.redemptions_count,
		       c.created_at, c.updated_at
		FROM campaign_codes cc
		JOIN campaigns c ON c.id = cc.campaign_id
		WHERE cc.tenant_id = $1 AND cc.code = $2
			AND c.status = 'active'
			AND now() >= c.starts_at AND now() < c.ends_at`,
		tenantID, code).
		Scan(&cc.ID, &cc.CampaignID, &cc.Code, &cc.MaxUses, &cc.UsesCount,
			&c.ID, &c.Name, &c.Status, &c.RuleType, &c.RuleValue, &c.ScopeType,
			&c.ScopeID, &c.RequiresCode, &c.StartsAt, &c.EndsAt,
			&c.MaxRedemptions, &c.RedemptionsCount, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CampaignCode{}, Campaign{}, fmt.Errorf("%w: %s", ErrInvalidCoupon, code)
	}
	if err != nil {
		return CampaignCode{}, Campaign{}, err
	}
	return cc, c, nil
}

// priceLines resolves campaign pricing for an order — the single source of
// truth for final prices, shared by checkout (apply=true) and read-only
// validation (apply=false).
//
// Selection: exactly ONE campaign applies per order (no stacking). Every
// live auto candidate plus a presented coupon's campaign competes on total
// order discount; ties go to the more specific scope, then to the coupon
// (explicitly requested), then lower id. A coupon that loses simply isn't
// consumed — it never errors and never stacks on top of a better auto deal.
//
// With apply=true the chosen campaign's redemption counter is bumped with
// a guarded single-row UPDATE (max_redemptions enforcement, O(1) critical
// section) and a campaign_redemptions row is inserted for the order.
// Lock order is campaign → code (products are locked later, sorted).
func priceLines(ctx context.Context, tx pgx.Tx, tenantID, orderID string,
	lines []ReserveLine, couponCode string, apply bool) (ReserveResult, error) {

	// product facts (category for scope matching, price_cents canonical)
	ids := make([]string, 0, len(lines))
	for _, l := range lines {
		ids = append(ids, l.ProductID)
	}
	type productFact struct {
		categoryID string
		priceCents int64
	}
	facts := make(map[string]productFact, len(ids))
	rows, err := tx.Query(ctx, `
		SELECT id, COALESCE(category_id::text, ''), price_cents
		FROM products WHERE tenant_id = $1 AND id = ANY($2)`, tenantID, ids)
	if err != nil {
		return ReserveResult{}, err
	}
	for rows.Next() {
		var id string
		var f productFact
		if err := rows.Scan(&id, &f.categoryID, &f.priceCents); err != nil {
			rows.Close()
			return ReserveResult{}, err
		}
		facts[id] = f
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ReserveResult{}, err
	}
	for _, l := range lines {
		if _, ok := facts[l.ProductID]; !ok {
			return ReserveResult{}, fmt.Errorf("product %s not found", l.ProductID)
		}
	}

	// candidates: a presented coupon's campaign first (wins ties), then
	// every live auto campaign (requires_code campaigns never auto-apply)
	type candidate struct {
		campaign   Campaign
		fromCoupon bool
		code       CampaignCode
	}
	auto, err := loadLiveCampaigns(ctx, tx, tenantID, false)
	if err != nil {
		return ReserveResult{}, err
	}
	candidates := make([]candidate, 0, len(auto)+1)
	presented := strings.ToUpper(strings.TrimSpace(couponCode))
	if presented != "" {
		cc, c, err := lookupCoupon(ctx, tx, tenantID, presented)
		if err != nil {
			return ReserveResult{}, err
		}
		candidates = append(candidates, candidate{campaign: c, fromCoupon: true, code: cc})
	}
	for _, c := range auto {
		// the same campaign could also be the coupon's — skip the dup
		if presented != "" && candidates[0].campaign.ID == c.ID {
			continue
		}
		candidates = append(candidates, candidate{campaign: c})
	}

	// pick the single best campaign by total order discount
	type priced struct {
		cand  candidate
		total int64
	}
	var best *priced
	better := func(a, b priced) bool {
		if a.total != b.total {
			return a.total > b.total
		}
		sa, sb := specificity(a.cand.campaign.ScopeType), specificity(b.cand.campaign.ScopeType)
		if sa != sb {
			return sa > sb
		}
		if a.cand.fromCoupon != b.cand.fromCoupon {
			return a.cand.fromCoupon // coupon wins ties vs auto
		}
		return a.cand.campaign.ID < b.cand.campaign.ID
	}
	for _, cand := range candidates {
		var total int64
		for _, l := range lines {
			f := facts[l.ProductID]
			if !scopeCovers(cand.campaign, l.ProductID, f.categoryID) {
				continue
			}
			total += ComputeDiscount(cand.campaign.RuleType, cand.campaign.RuleValue,
				f.priceCents) * int64(l.Quantity)
		}
		if total <= 0 {
			continue
		}
		p := priced{cand: cand, total: total}
		if best == nil || better(p, *best) {
			best = &p
		}
	}

	res := ReserveResult{
		Lines:    make([]PricedLine, 0, len(lines)),
		Campaign: nil,
	}
	var chosen *Campaign
	if best != nil {
		chosen = &best.cand.campaign
	}

	for _, l := range lines {
		f := facts[l.ProductID]
		final := f.priceCents
		if chosen != nil && scopeCovers(*chosen, l.ProductID, f.categoryID) {
			final = f.priceCents - ComputeDiscount(chosen.RuleType, chosen.RuleValue, f.priceCents)
		}
		res.Lines = append(res.Lines, PricedLine{
			ProductID: l.ProductID, ListPriceCents: f.priceCents, UnitFinalCents: final,
		})
	}
	if chosen == nil {
		return res, nil
	}
	res.Campaign = &AppliedCampaign{
		ID: chosen.ID, Name: chosen.Name, RuleType: chosen.RuleType,
		RuleValue: chosen.RuleValue, DiscountCents: best.total,
	}
	if best.cand.fromCoupon {
		res.Campaign.CouponCode = best.cand.code.Code
	}

	if !apply {
		return res, nil
	}

	// admission: guarded counter bump (campaign first, then code — the
	// global lock order); 0 rows means the cap refused us.
	tag, err := tx.Exec(ctx, `
		UPDATE campaigns
		SET redemptions_count = redemptions_count + 1, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND status = 'active'
			AND (max_redemptions IS NULL OR redemptions_count < max_redemptions)`,
		tenantID, chosen.ID)
	if err != nil {
		return ReserveResult{}, err
	}
	if tag.RowsAffected() == 0 {
		return ReserveResult{}, fmt.Errorf("%w: %s", ErrCampaignLimit, chosen.Name)
	}
	var codeID *string
	if best.cand.fromCoupon {
		tag, err := tx.Exec(ctx, `
			UPDATE campaign_codes
			SET uses_count = uses_count + 1
			WHERE tenant_id = $1 AND id = $2
				AND (max_uses IS NULL OR uses_count < max_uses)`,
			tenantID, best.cand.code.ID)
		if err != nil {
			return ReserveResult{}, err
		}
		if tag.RowsAffected() == 0 {
			return ReserveResult{}, fmt.Errorf("%w: code usage limit", ErrCampaignLimit)
		}
		codeID = &best.cand.code.ID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO campaign_redemptions
			(order_id, campaign_id, tenant_id, code_id, discount_cents)
		VALUES ($1, $2, $3, $4::uuid, $5)
		ON CONFLICT (order_id, campaign_id) DO NOTHING`,
		orderID, chosen.ID, tenantID, codeID, best.total); err != nil {
		return ReserveResult{}, err
	}
	return res, nil
}

// PriceOrder computes campaign pricing without reserving anything
// (validation-only gRPC calls). Read-only.
func (s *Store) PriceOrder(ctx context.Context, tenantID string,
	lines []ReserveLine, couponCode string) (ReserveResult, error) {
	var res ReserveResult
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		res, err = priceLines(ctx, tx, tenantID, "", lines, couponCode, false)
		return err
	})
	return res, err
}

// recountCampaign recomputes denormalized counters from the ledger for one
// campaign and its codes. Release paths are rare — recompute is cheap and
// self-heals any drift (the ledger is the source of truth).
func recountCampaign(ctx context.Context, tx pgx.Tx, tenantID, campaignID string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE campaigns c
		SET redemptions_count = (
			SELECT count(*) FROM campaign_redemptions r
			WHERE r.tenant_id = c.tenant_id AND r.campaign_id = c.id
				AND r.status = 'active'),
			updated_at = now()
		WHERE c.tenant_id = $1 AND c.id = $2`, tenantID, campaignID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE campaign_codes cc
		SET uses_count = (
			SELECT count(*) FROM campaign_redemptions r
			WHERE r.tenant_id = cc.tenant_id AND r.code_id = cc.id
				AND r.status = 'active')
		WHERE cc.tenant_id = $1 AND cc.campaign_id = $2`, tenantID, campaignID)
	return err
}

// releaseRedemptionsInTx releases an order's redemptions and recounts the
// affected campaigns (locks: redemption rows, then campaigns sorted).
func releaseRedemptionsInTx(ctx context.Context, tx pgx.Tx, tenantID, orderID string) error {
	rows, err := tx.Query(ctx, `
		SELECT campaign_id FROM campaign_redemptions
		WHERE tenant_id = $1 AND order_id = $2 AND status = 'active'
		FOR UPDATE`, tenantID, orderID)
	if err != nil {
		return err
	}
	var campaignIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		campaignIDs = append(campaignIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(campaignIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE campaign_redemptions
		SET status = 'released', updated_at = now()
		WHERE tenant_id = $1 AND order_id = $2 AND status = 'active'`,
		tenantID, orderID); err != nil {
		return err
	}
	sort.Strings(campaignIDs)
	for _, id := range campaignIDs {
		if err := recountCampaign(ctx, tx, tenantID, id); err != nil {
			return err
		}
	}
	return nil
}
