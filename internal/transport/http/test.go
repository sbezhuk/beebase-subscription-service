package http

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sbezhuk/beebase-common/httpx"
)

type testResponse struct {
	Status   string `json:"status"`
	Service  string `json:"service"`
	Database string `json:"database"`
}

// TestHandler handles the /test route. It verifies the database connection
// and returns a healthy JSON payload.
func TestHandler(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		var one int
		if err := db.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
			httpx.WriteJSON(w, http.StatusInternalServerError, testResponse{
				Status:   "error",
				Service:  "beebase-subscription",
				Database: "unreachable: " + err.Error(),
			})
			return
		}

		httpx.WriteJSON(w, http.StatusOK, testResponse{
			Status:   "ok",
			Service:  "beebase-subscription",
			Database: "connected",
		})
	}
}
