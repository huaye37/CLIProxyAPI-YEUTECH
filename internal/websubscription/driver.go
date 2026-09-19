package websubscription

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type DriverStatus string

const (
	DriverAvailable   DriverStatus = "available"
	DriverUnavailable DriverStatus = "unavailable"
	DriverDegraded    DriverStatus = "degraded"
)

type DriverInfo struct {
	ID              string            `json:"id"`
	DisplayName     string            `json:"display_name"`
	Implementation  string            `json:"implementation"`
	Version         string            `json:"version,omitempty"`
	ReferenceSource string            `json:"reference_source,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type ModelCapability struct {
	ID                string   `json:"id"`
	DisplayName       string   `json:"display_name"`
	InputModalities   []string `json:"input_modalities,omitempty"`
	OutputModalities  []string `json:"output_modalities,omitempty"`
	SupportsTools     bool     `json:"supports_tools"`
	SupportsParallel  bool     `json:"supports_parallel_tools"`
	SupportsStreaming bool     `json:"supports_streaming"`
	Available         bool     `json:"available"`
	Selectable        bool     `json:"selectable"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
}

type ProbeResult struct {
	Status DriverStatus      `json:"status"`
	Reason string            `json:"reason,omitempty"`
	Models []ModelCapability `json:"models,omitempty"`
}

type SessionRequest struct {
	AccountID string `json:"account_id"`
	Model     string `json:"model"`
	TraceID   string `json:"trace_id,omitempty"`
}

type EventType string

const (
	EventTextDelta EventType = "text_delta"
	EventToolCalls EventType = "tool_calls"
	EventCompleted EventType = "completed"
)

type GenerationEvent struct {
	Type    EventType    `json:"type"`
	Text    string       `json:"text,omitempty"`
	Actions []ToolAction `json:"actions,omitempty"`
	Error   error        `json:"-"`
}

type Session interface {
	Generate(context.Context, Turn) (<-chan GenerationEvent, error)
	Close() error
}

type SessionDriver interface {
	Info() DriverInfo
	Probe(context.Context) (ProbeResult, error)
	Open(context.Context, SessionRequest) (Session, error)
}

type DriverSnapshot struct {
	Info  DriverInfo  `json:"info"`
	Probe ProbeResult `json:"probe"`
	Error string      `json:"error,omitempty"`
}

type DriverRegistry struct {
	mu      sync.RWMutex
	drivers map[string]SessionDriver
}

func NewDriverRegistry() *DriverRegistry {
	return &DriverRegistry{drivers: make(map[string]SessionDriver)}
}

func (r *DriverRegistry) Register(driver SessionDriver) error {
	if driver == nil {
		return errors.New("driver is required")
	}
	info := driver.Info()
	info.ID = strings.TrimSpace(info.ID)
	if info.ID == "" {
		return errors.New("driver ID is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.drivers[info.ID]; exists {
		return fmt.Errorf("driver %q is already registered", info.ID)
	}
	r.drivers[info.ID] = driver
	return nil
}

func (r *DriverRegistry) Driver(id string) (SessionDriver, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	driver, ok := r.drivers[id]
	return driver, ok
}

func (r *DriverRegistry) ProbeAll(ctx context.Context) []DriverSnapshot {
	r.mu.RLock()
	drivers := make([]SessionDriver, 0, len(r.drivers))
	for _, driver := range r.drivers {
		drivers = append(drivers, driver)
	}
	r.mu.RUnlock()

	snapshots := make(chan DriverSnapshot, len(drivers))
	var wait sync.WaitGroup
	for _, driver := range drivers {
		wait.Add(1)
		go func(candidate SessionDriver) {
			defer wait.Done()
			snapshots <- probeDriver(ctx, candidate)
		}(driver)
	}
	wait.Wait()
	close(snapshots)

	result := make([]DriverSnapshot, 0, len(drivers))
	for snapshot := range snapshots {
		result = append(result, snapshot)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Info.ID < result[j].Info.ID })
	return result
}

func probeDriver(ctx context.Context, driver SessionDriver) (snapshot DriverSnapshot) {
	defer func() {
		if recovered := recover(); recovered != nil {
			snapshot.Probe = ProbeResult{Status: DriverUnavailable, Reason: "driver probe panicked"}
			snapshot.Error = fmt.Sprintf("driver probe panic: %v", recovered)
		}
	}()
	snapshot.Info = cloneDriverInfo(driver.Info())
	probe, err := driver.Probe(ctx)
	if err != nil {
		snapshot.Probe = ProbeResult{Status: DriverUnavailable, Reason: "driver probe failed"}
		snapshot.Error = err.Error()
		return snapshot
	}
	snapshot.Probe = cloneProbeResult(probe)
	return snapshot
}

func cloneDriverInfo(info DriverInfo) DriverInfo {
	if info.Metadata != nil {
		metadata := make(map[string]string, len(info.Metadata))
		for key, value := range info.Metadata {
			metadata[key] = value
		}
		info.Metadata = metadata
	}
	return info
}

func cloneProbeResult(probe ProbeResult) ProbeResult {
	probe.Models = append([]ModelCapability(nil), probe.Models...)
	for index := range probe.Models {
		probe.Models[index].InputModalities = append([]string(nil), probe.Models[index].InputModalities...)
		probe.Models[index].OutputModalities = append([]string(nil), probe.Models[index].OutputModalities...)
	}
	return probe
}
