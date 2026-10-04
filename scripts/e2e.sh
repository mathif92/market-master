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
