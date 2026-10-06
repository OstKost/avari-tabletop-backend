package bgg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientCancelRateLimit(t *testing.T) {
	for _, method := range []string{"search", "game"} {
		for _, preCanceled := range []bool{false, true} {
			name := method + "/waiting"
			if preCanceled {
				name = method + "/already canceled"
			}
			t.Run(name, func(t *testing.T) {
				var requests atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					_, _ = w.Write([]byte(`<items/>`))
				}))
				defer srv.Close()
				client := NewClientWithBaseURL(srv.URL)
				// No token can arrive: completing requires observing context cancellation.
				client.rateLimiter = make(chan time.Time)
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
				defer cancel()
				wantErr := context.DeadlineExceeded
				if preCanceled {
					cancel()
					wantErr = context.Canceled
				}
				completed := make(chan error, 1)
				go func() {
					var err error
					if method == "search" {
						_, err = client.Search(ctx, "catan")
					} else {
						_, err = client.GetGame(ctx, 13)
					}
					completed <- err
				}()
				select {
				case err := <-completed:
					require.ErrorIs(t, err, wantErr)
				case <-time.After(time.Second):
					t.Fatal("rate limit wait ignored canceled context")
				}
				assert.Zero(t, requests.Load())
			})
		}
	}
}
