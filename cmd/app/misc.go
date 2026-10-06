package main

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

func contextTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func jsonValid(data []byte) bool {
	return json.Valid(bytes.TrimSpace(data))
}
