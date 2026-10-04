-- campaign pricing display: keep the canonical pre-discount price on each
-- line so order pages can show strike-through pricing. Backfilled for
-- pre-campaign orders (no discount existed then: list == unit).

ALTER TABLE order_lines
	ADD COLUMN list_price_cents BIGINT NOT NULL DEFAULT 0
	CHECK (list_price_cents >= 0);

UPDATE order_lines SET list_price_cents = unit_price_cents;
