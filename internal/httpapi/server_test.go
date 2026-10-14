package httpapi

import (
	"encoding/json"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPointResponseIncludesMeasuredConsumption(t *testing.T) {
	p := domain.Point{ID: "p", Meter: domain.Meter{Registers: []domain.Register{{ID: "r", Unit: "m³"}}}, Readings: []domain.Reading{
		{RegisterID: "r", Date: "2026-10-05", Value: "12.125000"},
		{RegisterID: "r", Date: "2026-10-04", Value: "10.000000"},
	}}
	response, err := pointResponses([]domain.Point{p})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []struct {
		ID          string                       `json:"id"`
		Consumption []domain.ConsumptionInterval `json:"consumption"`
	}
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded[0].ID != "p" || len(decoded[0].Consumption) != 1 || decoded[0].Consumption[0].Quantity != "2.125000" {
		t.Fatal(string(data))
	}
}

func TestDecodeRejectsUnknownFieldsAndTrailingPayload(t *testing.T) {
	for _, input := range []string{`{"value":"1","secret":true}`, `{"value":"1"}{}`, strings.Repeat("x", (1<<20)+1)} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(input))
		var body struct {
			Value string `json:"value"`
		}
		if _, err := decode(r, &body); domain.Code(err) != "VALIDATION_FAILED" {
			t.Fatalf("accepted invalid body: %v", err)
		}
	}
}
func TestIdempotencyKeyRequired(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	for _, value := range []string{"", "invalid"} {
		r.Header.Set("Idempotency-Key", value)
		if _, err := key(r); domain.Code(err) != "IDEMPOTENCY_KEY_REQUIRED" {
			t.Fatal(err)
		}
	}
	r.Header.Set("Idempotency-Key", "11111111-1111-4111-8111-111111111111")
	if _, err := key(r); err != nil {
		t.Fatal(err)
	}
}

func TestPointOverviewWireContract(t *testing.T) {
	p := domain.Point{Version: 7, Meter: domain.Meter{Registers: []domain.Register{{ID: "i", Direction: "IMPORT", Unit: "kWh"}}}, Readings: []domain.Reading{{RegisterID: "i", Date: "2025-12-31", Value: "100"}, {RegisterID: "i", Date: "2026-01-01", Value: "101"}, {RegisterID: "i", Date: "2026-01-31", Value: "110"}}}
	rows, err := pointResponses([]domain.Point{p})
	if err != nil {
		t.Fatal(err)
	}
	rows[0].Annual = []domain.AnnualSummary{{Year: 2026, PointVersion: 7}}
	rows[0].withOverview()
	data, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		OverviewVersion int                    `json:"overview_version"`
		History         []domain.HistoryWindow `json:"history"`
		Readings        []domain.ReadingView   `json:"readings"`
		Annual          []domain.AnnualSummary `json:"annual"`
	}
	if err = json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if v.OverviewVersion != domain.OverviewVersion || len(v.History) != 1 || len(v.Readings) != 3 || !v.Readings[1].BoundaryPriority || v.Readings[1].Difference != nil || v.Readings[2].Difference == nil || *v.Readings[2].Difference != "10.000000" || len(v.Annual[0].Overview) != 1 {
		t.Fatalf("wire: %s", data)
	}
}

func TestMovePointRequiresSecondFactor(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest("POST", "/api/v1/workspaces/source/points/point/move", strings.NewReader(`{"target_workspace_id":"target","expected_version":1}`))
	err := server.movePoint(httptest.NewRecorder(), request, store.Session{Subject: "admin", ACR: "1"})
	if domain.Code(err) != "MFA_REQUIRED" {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	failure(response, domain.Fail("POINT_LINKED"))
	if response.Code != 409 {
		t.Fatalf("linked point status: %d", response.Code)
	}
}
