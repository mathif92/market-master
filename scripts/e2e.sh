#!/usr/bin/env bash
# End-to-end smoke test: tenant → login → catalog → order → pay → dispatch → deliver.
# Prerequisite: make up  (stack healthy on localhost:8080)
set -euo pipefail

GW=${GW:-http://localhost:8080}
SLUG=${SLUG:-demo}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@demo.test}
ADMIN_PASS=${ADMIN_PASS:-password123}
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

printf '\nSMOKE PASSED\n'
