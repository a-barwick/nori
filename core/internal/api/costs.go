package api

import (
	"net/http"
	"time"

	"nori/internal/state/docstore"
	"nori/internal/usage"
)

type costReportResponse struct {
	usage.CostReport
	Entitlement *entitlementCostSummary `json:"entitlement,omitempty"`
}

func (a *Assistant) apiCosts(w http.ResponseWriter, r *http.Request) {
	days := 30
	if values, ok := r.URL.Query()["days"]; ok {
		if len(values) != 1 {
			writeInvalidCostRange(w)
			return
		}
		switch values[0] {
		case "1":
			days = 1
		case "7":
			days = 7
		case "30":
		default:
			writeInvalidCostRange(w)
			return
		}
	}
	ctx := r.Context()
	if userDataResetPending(ctx) {
		writeError(w, docstore.ErrUserDataResetPending)
		return
	}
	prefs, _, err := a.Connections.Preferences(ctx)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	location, err := time.LoadLocation(prefs.Timezone)
	if err != nil || prefs.Timezone == "Local" {
		location = time.UTC
	}
	now := time.Now
	if a.UsageNow != nil {
		now = a.UsageNow
	}
	report, err := usage.ReadCostReport(ctx, a.Store.ReadUsageCosts, days, location, now())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	entSummary, err := a.buildEntitlementCostSummary(ctx)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, http.StatusOK, costReportResponse{
		CostReport:  report,
		Entitlement: entSummary,
	})
}

func writeInvalidCostRange(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, apiError{Error: "Choose a range of 1, 7, or 30 days.", Code: "validation_failed"})
}
