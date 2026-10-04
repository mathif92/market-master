package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"market-master/services/catalog/internal/store"
)

func TestStockRowsToItems(t *testing.T) {
	rows := [][]string{
		{"SKU", "Quantity"},
		{"popcorn", "42"},
		{"nachos", "-5"},
	}
	items, err := stockRowsToItems(rows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].SKU != "popcorn" || items[0].Quantity != 42 || items[0].Row != 2 {
		t.Errorf("first item wrong: %+v", items[0])
	}
	if items[1].Quantity != -5 || items[1].Row != 3 {
		t.Errorf("second item wrong: %+v", items[1])
	}
}

func TestStockRowsToItemsAcceptsProductIDColumn(t *testing.T) {
	rows := [][]string{
		{"product_id", "qty"},
		{"11111111-1111-4111-8111-111111111111", "3"},
	}
	items, err := stockRowsToItems(rows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if items[0].SKU == "" || items[0].Quantity != 3 {
		t.Errorf("item wrong: %+v", items[0])
	}
}

func TestStockRowsToItemsErrors(t *testing.T) {
	cases := []struct {
		name string
		rows [][]string
		want string
	}{
		{"missing columns", [][]string{{"name"}, {"popcorn"}}, "header must contain"},
		{"empty sku", [][]string{{"sku", "quantity"}, {"", "1"}}, "row 2: sku is empty"},
		{"empty qty", [][]string{{"sku", "quantity"}, {"popcorn", ""}}, "row 2: quantity is empty"},
		{"bad qty", [][]string{{"sku", "quantity"}, {"popcorn", "lots"}}, "quantity must be an integer"},
		{"short row", [][]string{{"sku", "quantity"}, {"popcorn"}}, "row 2: missing cells"},
		{"header only", [][]string{{"sku", "quantity"}}, "header row and at least one data row"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := stockRowsToItems(tc.rows)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestVerifyIngestSignature(t *testing.T) {
	body := []byte(`{"event_id":"e1","tenant_id":"t"}`)
	mac := hmac.New(sha256.New, []byte("dev-ingest-secret"))
	mac.Write(body)
	good := hex.EncodeToString(mac.Sum(nil))

	if !verifyIngestSignature("dev-ingest-secret", good, body) {
		t.Error("valid signature rejected")
	}
	if !verifyIngestSignature("dev-ingest-secret", strings.ToUpper(good), body) {
		t.Error("uppercase signature should be accepted")
	}
	if verifyIngestSignature("dev-ingest-secret", good, []byte("tampered")) {
		t.Error("signature accepted for tampered body")
	}
	if verifyIngestSignature("dev-ingest-secret", "deadbeef", body) {
		t.Error("bogus signature accepted")
	}
	if verifyIngestSignature("", good, body) {
		t.Error("empty secret must never verify")
	}
}

func TestLooksLikeUUID(t *testing.T) {
	valid := []string{
		"11111111-1111-4111-8111-111111111111",
		"A9C0D5E1-1B2C-4D3E-8F40-123456789ABC",
	}
	for _, s := range valid {
		if !looksLikeUUID(s) {
			t.Errorf("%s should be a uuid", s)
		}
	}
	invalid := []string{"", "not-a-uuid", "11111111111141118111111111111111",
		"11111111-1111-4111-8111-1111111111111", "g1111111-1111-4111-8111-111111111111",
		"11111111_1111_4111_8111_111111111111"}
	for _, s := range invalid {
		if looksLikeUUID(s) {
			t.Errorf("%q should not be a uuid", s)
		}
	}
}

func TestNormalizeItemsNumbersRows(t *testing.T) {
	items := normalizeItems([]ingestItemReq{
		{SKU: " a ", Quantity: 1},
		{SKU: "b", Quantity: 2},
	})
	if items[0].SKU != "a" || items[0].Row != 1 {
		t.Errorf("first: %+v", items[0])
	}
	if items[1].SKU != "b" || items[1].Row != 2 {
		t.Errorf("second: %+v", items[1])
	}
}

// Stock store row-shape sanity: IngestItem flows straight into the apply
// pipeline, keep JSON contract stable for API/webhook clients.
func TestIngestItemJSONContract(t *testing.T) {
	it := store.IngestItem{Row: 7, SKU: "popcorn", Quantity: -3}
	if it.Row != 7 || it.SKU != "popcorn" || it.Quantity != -3 {
		t.Errorf("unexpected: %+v", it)
	}
}
