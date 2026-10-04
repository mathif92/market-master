#!/usr/bin/env bash
# End-to-end smoke test: tenant → login → catalog → order → pay → dispatch → deliver.
# Prerequisite: make up  (stack healthy on localhost:8080)
set -euo pipefail

GW=${GW:-http://localhost:8080}
SLUG=${SLUG:-demo}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@demo.test}
ADMIN_PASS=${ADMIN_PASS:-password123}
PLATFORM_EMAIL=${PLATFORM_EMAIL:-platform@market.test}
PLATFORM_PASS=${PLATFORM_PASS:-platform12345}
RUN_KEY="$(date +%s)-$RANDOM"

say()  { printf '\n=== %s\n' "$*"; }
die()  { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

jget() { # jget '.field.sub' < json
  python3 -c '
import json, sys
v = json.load(sys.stdin)
for k in sys.argv[1].lstrip(".").split("."):
    v = v[k] if isinstance(v, dict) else v[int(k)]
print(v)' "$1"
}

# split_args: sets BODY and EXTRA from "method path [body] [curl args...]"
split_args() {
  BODY=""; EXTRA=()
  if [ $# -gt 0 ] && [[ "$1" != -* ]]; then BODY=$1; shift; fi
  EXTRA=("$@")
}

# request METHOD PATH [json-body] [extra curl args...]
request() {
  local method=$1 path=$2; shift 2
  split_args "$@"
  local args=(-sS -X "$method" "$GW$path" -H 'Content-Type: application/json')
  [ -n "${TOKEN:-}" ] && args+=(-H "Authorization: Bearer $TOKEN")
  [ -n "$SLUG" ] && args+=(-H "X-Tenant-Slug: $SLUG")
  [ -n "$BODY" ] && args+=(-d "$BODY")
  args+=("${EXTRA[@]+"${EXTRA[@]}"}")
  curl "${args[@]}"
}

status_of() { # status_of METHOD PATH [json-body] [extra curl args...] → http code
  local method=$1 path=$2; shift 2
  split_args "$@"
  local args=(-sS -o /dev/null -w '%{http_code}' -X "$method" "$GW$path" -H 'Content-Type: application/json')
  [ -n "${TOKEN:-}" ] && args+=(-H "Authorization: Bearer $TOKEN")
  [ -n "$SLUG" ] && args+=(-H "X-Tenant-Slug: $SLUG")
  [ -n "$BODY" ] && args+=(-d "$BODY")
  args+=("${EXTRA[@]+"${EXTRA[@]}"}")
  curl "${args[@]}"
}

wait_gateway() {
  say "waiting for gateway at $GW"
  for _ in $(seq 1 90); do
    if curl -s -o /dev/null "$GW/v1/products" 2>/dev/null; then return 0; fi
    sleep 2
  done
  die "gateway did not come up"
}

# poll_status ORDER_ID MIN_STATUS [attempts]
# Accepts MIN_STATUS or anything further along the happy path
# (paid → dispatched → delivered), since the event chain can complete
# in well under a second.
poll_status() {
  local id=$1 want=$2 tries=${3:-30} got rank want_rank
  case "$want" in
    created) want_rank=0 ;;
    awaiting_payment) want_rank=1 ;;
    payment_failed|paid) want_rank=2 ;;
    dispatched) want_rank=3 ;;
    delivered) want_rank=4 ;;
    cancelled) want_rank=5 ;;
    *) want_rank=0 ;;
  esac
  rank() {
    case "$1" in
      created) echo 0 ;;
      awaiting_payment) echo 1 ;;
      payment_failed|paid) echo 2 ;;
      dispatched) echo 3 ;;
      delivered) echo 4 ;;
      cancelled) echo 5 ;;
      *) echo -1 ;;
    esac
  }
  for _ in $(seq 1 "$tries"); do
    got=$(request GET "/v1/orders/$id" | jget .order.status || true)
    r=$(rank "$got")
    # dead-end states only satisfy an exact match; otherwise require the
    # happy path to have reached at least the expected stage
    if { [ "$got" = "$want" ]; } || \
       { [ "$r" -ge "$want_rank" ] && [ "$want" != "payment_failed" ] && [ "$want" != "cancelled" ] && [ "$got" != "payment_failed" ] && [ "$got" != "cancelled" ]; }; then
      echo "$got"; return 0
    fi
    sleep 1
  done
  die "order $id stuck at status '${got}', wanted '${want}'"
}

wait_gateway

say "create market '$SLUG'"
code=$(status_of POST /v1/tenants \
  "{\"slug\":\"$SLUG\",\"name\":\"Demo Market\",\"admin\":{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}}")
case "$code" in
  201) echo "tenant created" ;;
  409) echo "tenant exists (rerun)" ;;
  *)   die "create tenant returned $code" ;;
esac

say "login"
TOKEN=$(request POST /v1/auth/login \
  "{\"tenant_slug\":\"$SLUG\",\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" | jget .access_token)
[ -n "$TOKEN" ] || die "no token"
echo "got token"

say "create category + product"
CAT_ID=$(request POST /v1/categories "{\"name\":\"Snacks-$RUN_KEY\"}" | jget .id)
PROD_ID=$(request POST /v1/products \
  "{\"name\":\"Popcorn-$RUN_KEY\",\"price_cents\":1500,\"category_id\":\"$CAT_ID\"}" | jget .id)
echo "category=$CAT_ID product=$PROD_ID"

say "create order (with Idempotency-Key replay check)"
ORDER_BODY="{\"lines\":[{\"product_id\":\"$PROD_ID\",\"quantity\":2}],\"shipping\":{\"recipient_name\":\"Ada Lovelace\",\"line1\":\"1 Main St\",\"city\":\"Springfield\",\"country\":\"US\",\"postal_code\":\"12345\"}}"
KEY="order-$RUN_KEY"
HDR1=$(mktemp); HDR2=$(mktemp)
ORD1=$(curl -sS -D "$HDR1" -X POST "$GW/v1/orders" \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  -H "X-Tenant-Slug: $SLUG" -H "Idempotency-Key: $KEY" -d "$ORDER_BODY")
ORD2=$(curl -sS -D "$HDR2" -X POST "$GW/v1/orders" \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  -H "X-Tenant-Slug: $SLUG" -H "Idempotency-Key: $KEY" -d "$ORDER_BODY")
ORDER_ID=$(echo "$ORD1" | jget .order.id)
ORDER_ID2=$(echo "$ORD2" | jget .order.id)
[ "$ORDER_ID" = "$ORDER_ID2" ] || die "idempotent replay created a different order"
grep -qi 'Idempotency-Replayed: true' "$HDR2" || die "second call was not a replay"
echo "order=$ORDER_ID (replay verified)"

say "pay the order (auto-capture)"
PAY_CODE=$(status_of POST /v1/payments \
  "{\"order_id\":\"$ORDER_ID\",\"payment_method_token\":\"tok_visa_ok\"}" \
  -H "Idempotency-Key: pay-$RUN_KEY")
[ "$PAY_CODE" = "201" ] || die "payment returned $PAY_CODE"
poll_status "$ORDER_ID" paid
echo "order is paid"

say "wait for logistics dispatch"
poll_status "$ORDER_ID" dispatched
SHIPMENT_ID=$(request GET "/v1/shipments?order_id=$ORDER_ID" | jget .shipments.0.id)
echo "shipment=$SHIPMENT_ID"

say "mark delivered"
request POST "/v1/shipments/$SHIPMENT_ID/deliver" >/dev/null
poll_status "$ORDER_ID" delivered
echo "order delivered"

say "declined card path"
ORD3=$(request POST /v1/orders "$ORDER_BODY" -H "Idempotency-Key: order-decline-$RUN_KEY")
ORDER3_ID=$(echo "$ORD3" | jget .order.id)
PAY_CODE=$(status_of POST /v1/payments \
  "{\"order_id\":\"$ORDER3_ID\",\"payment_method_token\":\"tok_visa_decline\"}" \
  -H "Idempotency-Key: pay-decline-$RUN_KEY")
[ "$PAY_CODE" = "402" ] || die "declined payment returned $PAY_CODE, want 402"
poll_status "$ORDER3_ID" payment_failed
echo "decline handled"

say "cancel a pending order"
ORD4=$(request POST /v1/orders "$ORDER_BODY" -H "Idempotency-Key: order-cancel-$RUN_KEY")
ORDER4_ID=$(echo "$ORD4" | jget .order.id)
request POST "/v1/orders/$ORDER4_ID/cancel" >/dev/null
poll_status "$ORDER4_ID" cancelled
echo "cancelled"

# --- stock: enforcement + ingestion -------------------------------------
level_get() { # level_get FIELD → prints field for $PROD_ID
  request GET "/v1/inventory/levels" | python3 -c '
import json, sys
pid, field = sys.argv[1], sys.argv[2]
for lv in json.load(sys.stdin)["levels"]:
    if lv["product_id"] == pid:
        print(lv[field]); break
else:
    sys.exit("level not found")' "$PROD_ID" "$1"
}

poll_level() { # poll_level FIELD WANTED [tries]
  local field=$1 want=$2 tries=${3:-30} got=""
  for _ in $(seq 1 "$tries"); do
    got=$(level_get "$field" 2>/dev/null || true)
    [ "$got" = "$want" ] && { echo "$got"; return 0; }
    sleep 1
  done
  die "stock $field stuck at '$got', wanted '$want'"
}

SKU=$(request GET "/v1/products/$PROD_ID" | jget .slug)
LOGIN_JSON=$(request POST /v1/auth/login \
  "{\"tenant_slug\":\"$SLUG\",\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}")
TENANT_ID=$(echo "$LOGIN_JSON" | jget .user.tenant_id)
[ -n "$TENANT_ID" ] || die "no tenant_id in login response"

say "stock: start tracking via JSON ingest (set 5) + idempotent replay"
ING_BODY="{\"mode\":\"set\",\"items\":[{\"sku\":\"$SKU\",\"quantity\":5}]}"
code=$(status_of POST /v1/inventory/ingest "$ING_BODY" -H "Idempotency-Key: ing-$RUN_KEY")
[ "$code" = "200" ] || die "ingest returned $code"
code=$(status_of POST /v1/inventory/ingest "$ING_BODY" -H "Idempotency-Key: ing-$RUN_KEY")
[ "$code" = "200" ] || die "ingest replay returned $code"
poll_level available 5
echo "tracked, available=5"

say "stock: over-order is rejected with 422 insufficient_stock"
OVER_BODY="{\"lines\":[{\"product_id\":\"$PROD_ID\",\"quantity\":10}],\"shipping\":{\"recipient_name\":\"Ada Lovelace\",\"line1\":\"1 Main St\",\"city\":\"Springfield\",\"country\":\"US\",\"postal_code\":\"12345\"}}"
ERR=$(request POST /v1/orders "$OVER_BODY" -H "Idempotency-Key: over-$RUN_KEY")
code=$(printf '%s' "$ERR" | jget .error.code)
[ "$code" = "insufficient_stock" ] || die "over-order error code '$code', want insufficient_stock"
echo "422 with insufficient_stock verified"

say "stock: webhook ingest (HMAC) resolves product by id, replay is a no-op"
WEB_BODY="{\"event_id\":\"evt-$RUN_KEY\",\"tenant_id\":\"$TENANT_ID\",\"mode\":\"set\",\"items\":[{\"sku\":\"$PROD_ID\",\"quantity\":20}]}"
SIG=$(printf '%s' "$WEB_BODY" | openssl dgst -sha256 -hmac "dev-ingest-secret" | awk '{print $NF}')
code=$(status_of POST /v1/inventory/webhook "$WEB_BODY" -H "X-Ingest-Signature: bogus")
[ "$code" = "401" ] || die "bad webhook signature returned $code, want 401"
code=$(status_of POST /v1/inventory/webhook "$WEB_BODY" -H "X-Ingest-Signature: $SIG")
[ "$code" = "200" ] || die "webhook returned $code"
WEB2=$(request POST /v1/inventory/webhook "$WEB_BODY" -H "X-Ingest-Signature: $SIG")
replayed=$(echo "$WEB2" | jget .replayed)
[ "$replayed" = "True" ] || die "webhook replay not marked replayed ($replayed)"
poll_level available 20
echo "webhook applied + replay verified"

say "stock: paid order consumes stock (on_hand 20 → 18, reserved → 0)"
ORD5=$(request POST /v1/orders "$ORDER_BODY" -H "Idempotency-Key: order-stock-$RUN_KEY")
ORDER5_ID=$(echo "$ORD5" | jget .order.id)
poll_level reserved 2   # hold taken at order create (ORDER_BODY is qty 2)
PAY_CODE=$(status_of POST /v1/payments \
  "{\"order_id\":\"$ORDER5_ID\",\"payment_method_token\":\"tok_visa_ok\"}" \
  -H "Idempotency-Key: pay-stock-$RUN_KEY")
[ "$PAY_CODE" = "201" ] || die "payment returned $PAY_CODE"
poll_status "$ORDER5_ID" paid
poll_level on_hand 18
poll_level reserved 0
poll_level available 18
echo "sale committed"

say "stock: declined payment KEEPS the hold (retry-safe)"
ORD6=$(request POST /v1/orders "$ORDER_BODY" -H "Idempotency-Key: order-decline-stock-$RUN_KEY")
ORDER6_ID=$(echo "$ORD6" | jget .order.id)
poll_level reserved 2
PAY_CODE=$(status_of POST /v1/payments \
  "{\"order_id\":\"$ORDER6_ID\",\"payment_method_token\":\"tok_visa_decline\"}" \
  -H "Idempotency-Key: pay-decline-stock-$RUN_KEY")
[ "$PAY_CODE" = "402" ] || die "declined payment returned $PAY_CODE"
poll_status "$ORDER6_ID" payment_failed
sleep 3   # give the consumer a beat to (not) act on payment_failed
poll_level reserved 2
poll_level on_hand 18
echo "hold survived the decline"

say "stock: cancel releases holds per-order"
ORD7=$(request POST /v1/orders "$ORDER_BODY" -H "Idempotency-Key: order-cancel-stock-$RUN_KEY")
ORDER7_ID=$(echo "$ORD7" | jget .order.id)
poll_level reserved 4   # ORD6's 2 + ORD7's 2
request POST "/v1/orders/$ORDER7_ID/cancel" >/dev/null
poll_status "$ORDER7_ID" cancelled
poll_level reserved 2   # only ORD7 released; ORD6 still held
request POST "/v1/orders/$ORDER6_ID/cancel" >/dev/null
poll_status "$ORDER6_ID" cancelled
poll_level reserved 0
poll_level on_hand 18
echo "cancel released"

say "stock: CSV upload applies once (content-hash replay)"
CSV_FILE=$(mktemp --suffix=.csv)
printf 'sku,quantity\n%s,7\n' "$SKU" > "$CSV_FILE"
UP1=$(curl -sS -X POST "$GW/v1/inventory/upload" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-Slug: $SLUG" \
  -F "mode=set" -F "file=@$CSV_FILE")
applied=$(echo "$UP1" | jget .applied_rows)
[ "$applied" = "1" ] || die "upload applied_rows=$applied, want 1"
UP2=$(curl -sS -X POST "$GW/v1/inventory/upload" \
  -H "Authorization: Bearer $TOKEN" -H "X-Tenant-Slug: $SLUG" \
  -F "mode=set" -F "file=@$CSV_FILE")
replayed=$(echo "$UP2" | jget .replayed)
[ "$replayed" = "True" ] || die "upload replay not marked replayed ($replayed)"
poll_level on_hand 7
poll_level available 7
rm -f "$CSV_FILE"
echo "upload dedup verified"

say "stock: movements ledger has entries"
MV=$(request GET "/v1/inventory/movements?product_id=$PROD_ID&limit=50")
n=$(echo "$MV" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["movements"]))')
[ "$n" -ge 4 ] || die "expected >=4 movements, got $n"
echo "movements=$n"

# --- campaigns: discounts, coupons, caps --------------------------------
camp_field() { # camp_field ID FIELD → prints field of a campaign from the admin list
  request GET /v1/campaigns | python3 -c '
import json, sys
cid, field = sys.argv[1], sys.argv[2]
for c in json.load(sys.stdin)["campaigns"]:
    if c["id"] == cid:
        v = c
        for k in field.split("."):
            v = v[k] if isinstance(v, dict) else v[int(k)]
        print(v); break
else:
    sys.exit("campaign not found")' "$1" "$2"
}

STARTS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ENDS=$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)

say "campaigns: sitewide 50% campaign + read-time sale price"
CAMP_BODY="{\"name\":\"Smoke Sale-$RUN_KEY\",\"status\":\"active\",\"rule_type\":\"percent\",\"rule_value\":5000,\"scope_type\":\"sitewide\",\"requires_code\":false,\"starts_at\":\"$STARTS\",\"ends_at\":\"$ENDS\"}"
CAMP1=$(request POST /v1/campaigns "$CAMP_BODY" -H "Idempotency-Key: camp-$RUN_KEY")
CAMP1_ID=$(echo "$CAMP1" | jget .id)
[ -n "$CAMP1_ID" ] || die "campaign create returned no id"
CAMP1R=$(request POST /v1/campaigns "$CAMP_BODY" -H "Idempotency-Key: camp-$RUN_KEY")
[ "$(echo "$CAMP1R" | jget .id)" = "$CAMP1_ID" ] || die "campaign idempotent replay created a second campaign"
echo "campaign=$CAMP1_ID (replay verified)"

P=$(request GET "/v1/products/$PROD_ID")
[ "$(echo "$P" | jget .sale_price_cents)" = "750" ] || die "sale_price_cents != 750"
[ "$(echo "$P" | jget .price_cents)" = "1500" ] || die "price_cents was rewritten by the campaign"
[ "$(echo "$P" | jget .campaign.id)" = "$CAMP1_ID" ] || die "product not annotated with the campaign"
echo "read-time sale price 750 (canonical price intact)"

ACTIVE=$(curl -sS "$GW/v1/campaigns/active" -H "X-Tenant-Slug: $SLUG")
echo "$ACTIVE" | grep -q "$CAMP1_ID" || die "public /v1/campaigns/active missing the live campaign"
echo "public banner feed ok"

say "campaigns: reserve is atomic with pricing (shortage rolls back the slot)"
OVER100="{\"lines\":[{\"product_id\":\"$PROD_ID\",\"quantity\":100}],\"shipping\":{\"recipient_name\":\"Ada Lovelace\",\"line1\":\"1 Main St\",\"city\":\"Springfield\",\"country\":\"US\",\"postal_code\":\"12345\"}}"
ERR=$(request POST /v1/orders "$OVER100" -H "Idempotency-Key: over-camp-$RUN_KEY")
[ "$(echo "$ERR" | jget .error.code)" = "insufficient_stock" ] || die "over-order with campaign did not fail insufficient_stock"
[ "$(camp_field "$CAMP1_ID" redemptions_count)" = "0" ] || die "failed reserve left a redemption behind"
echo "stock shortage rolled back the campaign slot"

say "campaigns: discounted snapshot at checkout (list vs unit)"
ORD8=$(request POST /v1/orders "$ORDER_BODY" -H "Idempotency-Key: order-camp-$RUN_KEY")
ORDER8_ID=$(echo "$ORD8" | jget .order.id)
[ "$(echo "$ORD8" | python3 -c 'import json,sys; l=json.load(sys.stdin)["lines"][0]; print(l["unit_price_cents"])')" = "750" ] \
  || die "order unit_price_cents not discounted"
[ "$(echo "$ORD8" | python3 -c 'import json,sys; l=json.load(sys.stdin)["lines"][0]; print(l["list_price_cents"])')" = "1500" ] \
  || die "order list_price_cents missing"
[ "$(echo "$ORD8" | jget .order.total_cents)" = "1500" ] || die "discounted total != 1500"
[ "$(camp_field "$CAMP1_ID" redemptions_count)" = "1" ] || die "redemption not recorded"
poll_level reserved 2
PAY_CODE=$(status_of POST /v1/payments \
  "{\"order_id\":\"$ORDER8_ID\",\"payment_method_token\":\"tok_visa_ok\"}" \
  -H "Idempotency-Key: pay-camp-$RUN_KEY")
[ "$PAY_CODE" = "201" ] || die "campaign payment returned $PAY_CODE"
poll_status "$ORDER8_ID" paid
poll_level on_hand 5
poll_level reserved 0
echo "50% order paid; on_hand 5"

say "campaigns: archiving removes the read-time sale"
code=$(status_of DELETE "/v1/campaigns/$CAMP1_ID")
[ "$code" = "204" ] || die "archive returned $code"
P=$(request GET "/v1/products/$PROD_ID")
echo "$P" | python3 -c 'import json,sys; sys.exit(1 if json.load(sys.stdin).get("sale_price_cents") == 750 else 0)' \
  || die "archived campaign still shows a sale price"
echo "sale gone after archive"

say "campaigns: coupon codes (requires_code never auto-applies)"
COUPON_BODY="{\"name\":\"Coupon Sale-$RUN_KEY\",\"status\":\"active\",\"rule_type\":\"percent\",\"rule_value\":3000,\"scope_type\":\"sitewide\",\"requires_code\":true,\"starts_at\":\"$STARTS\",\"ends_at\":\"$ENDS\"}"
CAMP2_ID=$(request POST /v1/campaigns "$COUPON_BODY" -H "Idempotency-Key: camp2-$RUN_KEY" | jget .id)
CODES=$(request POST "/v1/campaigns/$CAMP2_ID/codes" '{"count":5,"max_uses":1}' -H "Idempotency-Key: codes-$RUN_KEY")
CODE=$(echo "$CODES" | jget .codes.0.code)
[ -n "$CODE" ] || die "no coupon code generated"
P=$(request GET "/v1/products/$PROD_ID")
echo "$P" | python3 -c 'import json,sys; sys.exit(0 if "sale_price_cents" not in json.load(sys.stdin) else 1)' \
  || die "requires_code campaign leaked into read-time prices"
ACTIVE=$(curl -sS "$GW/v1/campaigns/active" -H "X-Tenant-Slug: $SLUG")
echo "$ACTIVE" | grep -q "$CAMP2_ID" && die "requires_code campaign appeared in the public banner"
echo "code=$CODE (auto-apply correctly excluded)"

say "campaigns: invalid coupon is rejected without side effects"
BADC="{\"lines\":[{\"product_id\":\"$PROD_ID\",\"quantity\":1}],\"coupon_code\":\"NOPE-$RUN_KEY\",\"shipping\":{\"recipient_name\":\"Ada Lovelace\",\"line1\":\"1 Main St\",\"city\":\"Springfield\",\"country\":\"US\",\"postal_code\":\"12345\"}}"
ERR=$(request POST /v1/orders "$BADC" -H "Idempotency-Key: bad-coupon-$RUN_KEY")
[ "$(echo "$ERR" | jget .error.code)" = "invalid_coupon" ] || die "bad coupon returned '$(echo "$ERR" | jget .error.code)', want invalid_coupon"
echo "422 invalid_coupon verified"

say "campaigns: coupon applies once per code (max_uses=1)"
COUPON_ORD="{\"lines\":[{\"product_id\":\"$PROD_ID\",\"quantity\":2}],\"coupon_code\":\"$CODE\",\"shipping\":{\"recipient_name\":\"Ada Lovelace\",\"line1\":\"1 Main St\",\"city\":\"Springfield\",\"country\":\"US\",\"postal_code\":\"12345\"}}"
ORD9=$(request POST /v1/orders "$COUPON_ORD" -H "Idempotency-Key: order-coupon-$RUN_KEY")
ORDER9_ID=$(echo "$ORD9" | jget .order.id)
[ "$(echo "$ORD9" | python3 -c 'import json,sys; l=json.load(sys.stdin)["lines"][0]; print(l["unit_price_cents"])')" = "1050" ] \
  || die "coupon unit price != 1050"
code_field() { # code_field CODE FIELD
  request GET "/v1/campaigns/$CAMP2_ID/codes" | python3 -c '
import json, sys
code, field = sys.argv[1], sys.argv[2]
for c in json.load(sys.stdin)["codes"]:
    if c["code"] == code:
        print(c[field]); break
else:
    sys.exit("code not found")' "$1" "$2"
}
[ "$(code_field "$CODE" uses_count)" = "1" ] || die "code uses_count != 1"
ERR=$(request POST /v1/orders "$COUPON_ORD" -H "Idempotency-Key: order-coupon2-$RUN_KEY")
[ "$(echo "$ERR" | jget .error.code)" = "campaign_limit" ] || die "second code use did not fail campaign_limit"
[ "$(code_field "$CODE" uses_count)" = "1" ] || die "failed second use bumped the counter"
poll_level reserved 2
request POST "/v1/orders/$ORDER9_ID/cancel" >/dev/null
poll_status "$ORDER9_ID" cancelled
poll_level reserved 0
[ "$(code_field "$CODE" uses_count)" = "0" ] || die "cancel did not release the code slot (recount)"
echo "coupon: one use, released on cancel"

say "campaigns: max_redemptions cap + release refunds the slot"
CAP_BODY="{\"name\":\"Cap Sale-$RUN_KEY\",\"status\":\"active\",\"rule_type\":\"percent\",\"rule_value\":9000,\"scope_type\":\"sitewide\",\"requires_code\":false,\"starts_at\":\"$STARTS\",\"ends_at\":\"$ENDS\",\"max_redemptions\":1}"
CAMP3_ID=$(request POST /v1/campaigns "$CAP_BODY" -H "Idempotency-Key: camp3-$RUN_KEY" | jget .id)
ORD10=$(request POST /v1/orders "$ORDER_BODY" -H "Idempotency-Key: order-cap1-$RUN_KEY")
ORDER10_ID=$(echo "$ORD10" | jget .order.id)
[ "$(camp_field "$CAMP3_ID" redemptions_count)" = "1" ] || die "capped campaign count != 1"
ERR=$(request POST /v1/orders "$ORDER_BODY" -H "Idempotency-Key: order-cap2-$RUN_KEY")
[ "$(echo "$ERR" | jget .error.code)" = "campaign_limit" ] || die "over-cap order did not fail campaign_limit"
[ "$(camp_field "$CAMP3_ID" redemptions_count)" = "1" ] || die "failed over-cap order bumped the counter"
poll_level reserved 2
request POST "/v1/orders/$ORDER10_ID/cancel" >/dev/null
poll_status "$ORDER10_ID" cancelled
poll_level reserved 0
[ "$(camp_field "$CAMP3_ID" redemptions_count)" = "0" ] || die "cancel did not refund the redemption slot"
ORD11=$(request POST /v1/orders "$ORDER_BODY" -H "Idempotency-Key: order-cap3-$RUN_KEY")
ORDER11_ID=$(echo "$ORD11" | jget .order.id)
[ "$(camp_field "$CAMP3_ID" redemptions_count)" = "1" ] || die "refunded slot not reusable"
request POST "/v1/orders/$ORDER11_ID/cancel" >/dev/null
poll_status "$ORDER11_ID" cancelled
poll_level reserved 0
poll_level on_hand 5
echo "cap enforced, slot refunded and reused"

say "platform: panel endpoints reject non-platform callers"
code=$(status_of GET /v1/tenants)   # tenant admin token
[ "$code" = "403" ] || die "tenant admin listed markets, got $code"
code=$(curl -sS -o /dev/null -w '%{http_code}' "$GW/v1/tenants")  # no token
[ "$code" = "401" ] || die "unauthenticated market list, got $code"

say "platform: login as bootstrap platform admin"
OLD_TOKEN=$TOKEN
TOKEN=$(request POST /v1/auth/login \
  "{\"tenant_slug\":\"\",\"email\":\"$PLATFORM_EMAIL\",\"password\":\"$PLATFORM_PASS\"}" | jget .access_token)
[ -n "$TOKEN" ] || die "no platform token"

say "platform: list markets"
TENANTS=$(request GET /v1/tenants)
PLAT_ID=$(printf '%s' "$TENANTS" | python3 -c '
import json, sys
slug = sys.argv[1]
for t in json.load(sys.stdin)["tenants"]:
    if t["slug"] == slug:
        print(t["id"]); break
else:
    sys.exit("market not found: " + slug)' "$SLUG")
echo "found $SLUG=$PLAT_ID"

say "platform: suspend demo market → market-scoped traffic blocked"
code=$(status_of PATCH "/v1/tenants/$PLAT_ID" '{"status":"suspended"}')
[ "$code" = "200" ] || die "suspend returned $code"
# gateway caches tenant status briefly (TENANT_CACHE_TTL) — poll for the 403
code=200
for _ in $(seq 1 30); do
  code=$(status_of GET /v1/products)   # X-Tenant-Slug: $SLUG still set
  [ "$code" = "403" ] && break
  sleep 1
done
[ "$code" = "403" ] || die "suspended market still served traffic, got $code"

say "platform: reactivate demo market"
code=$(status_of PATCH "/v1/tenants/$PLAT_ID" '{"status":"active"}')
[ "$code" = "200" ] || die "reactivate returned $code"
code=403
for _ in $(seq 1 30); do
  code=$(status_of GET /v1/products)
  [ "$code" = "200" ] && break
  sleep 1
done
[ "$code" = "200" ] || die "reactivated market still blocked, got $code"
echo "suspend/reactivate verified"

say "platform: invite a second platform admin"
PU_EMAIL="ops-$RUN_KEY@platform.test"
code=$(status_of POST /v1/platform/users \
  "{\"email\":\"$PU_EMAIL\",\"password\":\"platform-pass-1\"}" \
  -H "Idempotency-Key: pu-$RUN_KEY")
[ "$code" = "201" ] || die "create platform user returned $code"
code=$(status_of POST /v1/platform/users \
  "{\"email\":\"$PU_EMAIL\",\"password\":\"platform-pass-1\"}" \
  -H "Idempotency-Key: pu-$RUN_KEY")
[ "$code" = "201" ] || die "idempotent platform user replay returned $code"
PU_ID=$(request GET /v1/platform/users | python3 -c '
import json, sys
for u in json.load(sys.stdin)["users"]:
    if u["email"] == sys.argv[1]:
        print(u["id"]); break
else:
    sys.exit("platform user not found")' "$PU_EMAIL")
echo "platform user=$PU_ID"

say "platform: new admin can log in; disabled admin cannot"
PU_TOKEN=$(request POST /v1/auth/login \
  "{\"tenant_slug\":\"\",\"email\":\"$PU_EMAIL\",\"password\":\"platform-pass-1\"}" | jget .access_token)
[ -n "$PU_TOKEN" ] || die "second platform admin cannot log in"
TOKEN=$PU_TOKEN
code=$(status_of GET /v1/tenants)  # fresh admin is also a platform admin
[ "$code" = "200" ] || die "second platform admin cannot list markets, got $code"
TOKEN=$(request POST /v1/auth/login \
  "{\"tenant_slug\":\"\",\"email\":\"$PLATFORM_EMAIL\",\"password\":\"$PLATFORM_PASS\"}" | jget .access_token)
code=$(status_of PATCH "/v1/platform/users/$PU_ID" '{"status":"disabled"}')
[ "$code" = "200" ] || die "disable returned $code"
code=$(status_of POST /v1/auth/login \
  "{\"tenant_slug\":\"\",\"email\":\"$PU_EMAIL\",\"password\":\"platform-pass-1\"}")
[ "$code" = "401" ] || die "disabled platform admin still logged in, got $code"
echo "invite/disable verified"
TOKEN=$OLD_TOKEN

printf '\nSMOKE PASSED\n'
