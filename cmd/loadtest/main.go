package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type options struct {
	baseURL     string
	eventID     string
	profile     string
	duration    time.Duration
	warmup      time.Duration
	concurrency int
	timeout     time.Duration
	seatID      string
	sectionID   string
	seed        int64
}

type sample struct {
	latency time.Duration
	status  int
	code    string
	success bool
}

type report struct {
	Profile       string         `json:"profile"`
	BaseURL       string         `json:"base_url"`
	EventID       string         `json:"event_id"`
	StartedAt     time.Time      `json:"started_at"`
	Warmup        string         `json:"warmup"`
	Duration      string         `json:"duration"`
	Concurrency   int            `json:"concurrency"`
	Seed          int64          `json:"seed"`
	Operations    int            `json:"operations"`
	HTTPRequests  int64          `json:"http_requests"`
	Succeeded     int            `json:"succeeded"`
	Failed        int            `json:"failed"`
	ThroughputRPS float64        `json:"operations_per_second"`
	P50MS         float64        `json:"p50_ms"`
	P95MS         float64        `json:"p95_ms"`
	P99MS         float64        `json:"p99_ms"`
	MaxMS         float64        `json:"max_ms"`
	StatusCounts  map[string]int `json:"status_counts"`
	ErrorCodes    map[string]int `json:"error_codes"`
}

type runner struct {
	options  options
	client   *http.Client
	count    atomic.Int64
	sequence atomic.Int64
	runID    string
}

type apiResponse struct {
	Kind    string `json:"kind"`
	Code    string `json:"code"`
	Booking *struct {
		ID string `json:"booking_id"`
	} `json:"booking"`
	HoldID string `json:"hold_id"`
}

func main() {
	var value options
	flag.StringVar(&value.baseURL, "base-url", "http://127.0.0.1:8080", "ticketing API base URL")
	flag.StringVar(&value.eventID, "event", "100", "event ID")
	flag.StringVar(&value.profile, "profile", "entry", "entry, hold-auto, or hold-hot")
	flag.DurationVar(&value.duration, "duration", 10*time.Second, "measured duration")
	flag.DurationVar(&value.warmup, "warmup", 2*time.Second, "unmeasured warmup duration")
	flag.IntVar(&value.concurrency, "concurrency", 16, "concurrent workers")
	flag.DurationVar(&value.timeout, "timeout", 3*time.Second, "per-operation timeout")
	flag.StringVar(&value.seatID, "seat", "1", "seat ID for hold-hot")
	flag.StringVar(&value.sectionID, "section", "10", "section ID for hold-auto")
	flag.Int64Var(&value.seed, "seed", 20260924, "deterministic subject sequence seed")
	flag.Parse()
	if value.concurrency < 1 || value.duration <= 0 || value.warmup < 0 || value.timeout <= 0 {
		fatal("concurrency and durations must be positive")
	}
	if value.profile != "entry" && value.profile != "hold-auto" && value.profile != "hold-hot" {
		fatal("profile must be entry, hold-auto, or hold-hot")
	}
	value.baseURL = strings.TrimRight(value.baseURL, "/")
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: value.timeout}).DialContext,
		MaxIdleConns: value.concurrency * 2, MaxIdleConnsPerHost: value.concurrency * 2,
		MaxConnsPerHost: value.concurrency * 2, IdleConnTimeout: 30 * time.Second,
	}
	run := &runner{
		options: value, client: &http.Client{Transport: transport},
		runID: strconv.FormatInt(time.Now().UnixNano(), 36),
	}
	run.sequence.Store(value.seed)
	defer transport.CloseIdleConnections()

	if value.warmup > 0 {
		run.workload(value.warmup, false)
	}
	run.count.Store(0)
	started := time.Now().UTC()
	samples := run.workload(value.duration, true)
	result := summarize(value, started, run.count.Load(), samples)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fatal(err.Error())
	}
}

func (r *runner) workload(duration time.Duration, capture bool) []sample {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	results := make(chan sample, r.options.concurrency*4)
	var workers sync.WaitGroup
	for worker := 0; worker < r.options.concurrency; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for ctx.Err() == nil {
				sequence := r.sequence.Add(1)
				// Operations started inside the measurement window are allowed to finish;
				// the window boundary itself must not be reported as a server timeout.
				operationCtx, operationCancel := context.WithTimeout(context.Background(), r.options.timeout)
				started := time.Now()
				status, code, err := r.operation(operationCtx, worker, sequence)
				operationCancel()
				if capture {
					results <- sample{latency: time.Since(started), status: status, code: code, success: err == nil}
				}
			}
		}(worker)
	}
	go func() {
		workers.Wait()
		close(results)
	}()
	collected := make([]sample, 0)
	for result := range results {
		collected = append(collected, result)
	}
	return collected
}

func (r *runner) operation(ctx context.Context, worker int, sequence int64) (int, string, error) {
	subject := fmt.Sprintf("load-%s-%s-%d-%d", r.options.profile, r.runID, worker, sequence)
	entry, status, err := r.post(ctx, "/events/"+r.options.eventID+"/entry", subject, "", map[string]any{})
	if err != nil {
		return status, entry.Code, err
	}
	if entry.Kind != "DIRECT" || entry.Booking == nil {
		return http.StatusConflict, "QUEUED", fmt.Errorf("entry was queued")
	}
	bookingID := entry.Booking.ID
	defer r.postBestEffort("/booking/leave", subject, map[string]any{"event_id": r.options.eventID, "booking_id": bookingID})
	if r.options.profile == "entry" {
		return status, "", nil
	}
	body := map[string]any{"booking_id": bookingID, "quantity": 1}
	if r.options.profile == "hold-hot" {
		body["mode"] = "specified"
		body["seat_ids"] = []string{r.options.seatID}
	} else {
		body["mode"] = "auto"
		body["section_id"] = r.options.sectionID
	}
	hold, status, err := r.post(ctx, "/events/"+r.options.eventID+"/holds", subject, fmt.Sprintf("load-%s-%d-%d", r.runID, worker, sequence), body)
	if err != nil {
		return status, hold.Code, err
	}
	defer r.postBestEffort("/holds/"+hold.HoldID+"/cancel", subject, map[string]any{})
	return status, "", nil
}

func (r *runner) post(ctx context.Context, path, subject, idempotencyKey string, body any) (apiResponse, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return apiResponse{}, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.options.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return apiResponse{}, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Subject-ID", subject)
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	r.count.Add(1)
	response, err := r.client.Do(request)
	if err != nil {
		return apiResponse{}, 0, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return apiResponse{}, response.StatusCode, err
	}
	var result apiResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		return apiResponse{}, response.StatusCode, fmt.Errorf("decode response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, response.StatusCode, fmt.Errorf("server returned %d", response.StatusCode)
	}
	return result, response.StatusCode, nil
}

func (r *runner) postBestEffort(path, subject string, body any) {
	ctx, cancel := context.WithTimeout(context.Background(), r.options.timeout)
	defer cancel()
	_, _, _ = r.post(ctx, path, subject, "", body)
}

func summarize(value options, started time.Time, requestCount int64, samples []sample) report {
	result := report{
		Profile: value.profile, BaseURL: value.baseURL, EventID: value.eventID, StartedAt: started,
		Warmup: value.warmup.String(), Duration: value.duration.String(), Concurrency: value.concurrency, Seed: value.seed,
		Operations: len(samples), HTTPRequests: requestCount, StatusCounts: make(map[string]int), ErrorCodes: make(map[string]int),
	}
	latencies := make([]time.Duration, 0, len(samples))
	for _, item := range samples {
		latencies = append(latencies, item.latency)
		result.StatusCounts[strconv.Itoa(item.status)]++
		if item.success {
			result.Succeeded++
		} else {
			result.Failed++
			if item.code == "" {
				item.code = "TRANSPORT_ERROR"
			}
			result.ErrorCodes[item.code]++
		}
	}
	result.ThroughputRPS = float64(result.Operations) / value.duration.Seconds()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	result.P50MS = percentile(latencies, 0.50)
	result.P95MS = percentile(latencies, 0.95)
	result.P99MS = percentile(latencies, 0.99)
	result.MaxMS = percentile(latencies, 1)
	return result
}

func percentile(sorted []time.Duration, quantile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1)*quantile + 0.5)
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return float64(sorted[index].Microseconds()) / 1000
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(2)
}
