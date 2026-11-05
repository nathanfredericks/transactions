// Package browserbroker coordinates browser capacity without handling bank state.
package browserbroker

import "time"

type Request struct {
	Bank       string `json:"bank"`
	JobID      string `json:"jobId"`
	Generation string `json:"generation"`
}
type Reservation struct {
	ID string `json:"id"`
	Request
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type Status struct {
	Owner   string `json:"owner"`
	Ready   bool   `json:"ready"`
	Pending int    `json:"pending"`
}
