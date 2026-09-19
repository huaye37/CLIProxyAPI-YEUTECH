package websubscription

import (
	"context"
	"errors"
	"testing"
)

type stubDriver struct {
	info       DriverInfo
	probe      ProbeResult
	probeErr   error
	probePanic bool
}

func (d *stubDriver) Info() DriverInfo { return d.info }

func (d *stubDriver) Probe(context.Context) (ProbeResult, error) {
	if d.probePanic {
		panic("broken browser adapter")
	}
	return d.probe, d.probeErr
}

func (d *stubDriver) Open(context.Context, SessionRequest) (Session, error) {
	return nil, errors.New("not implemented")
}

func TestDriverRegistryRejectsDuplicates(t *testing.T) {
	registry := NewDriverRegistry()
	if err := registry.Register(&stubDriver{info: DriverInfo{ID: "gemini-web"}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&stubDriver{info: DriverInfo{ID: "gemini-web"}}); err == nil {
		t.Fatal("expected duplicate driver error")
	}
}

func TestDriverRegistryProbeIsolation(t *testing.T) {
	registry := NewDriverRegistry()
	drivers := []*stubDriver{
		{info: DriverInfo{ID: "chatgpt-web"}, probeErr: errors.New("browser is signed out")},
		{info: DriverInfo{ID: "gemini-web"}, probe: ProbeResult{Status: DriverAvailable, Models: []ModelCapability{{ID: "gemini-web-auto", Available: true, Selectable: true}}}},
		{info: DriverInfo{ID: "panic-web"}, probePanic: true},
	}
	for _, driver := range drivers {
		if err := registry.Register(driver); err != nil {
			t.Fatal(err)
		}
	}

	snapshots := registry.ProbeAll(context.Background())
	if len(snapshots) != 3 {
		t.Fatalf("snapshots = %d", len(snapshots))
	}
	if snapshots[0].Info.ID != "chatgpt-web" || snapshots[0].Probe.Status != DriverUnavailable {
		t.Fatalf("unexpected failed snapshot: %#v", snapshots[0])
	}
	if snapshots[1].Info.ID != "gemini-web" || !snapshots[1].Probe.Models[0].Selectable {
		t.Fatalf("healthy driver was affected: %#v", snapshots[1])
	}
	if snapshots[2].Info.ID != "panic-web" || snapshots[2].Probe.Status != DriverUnavailable {
		t.Fatalf("panic was not isolated: %#v", snapshots[2])
	}
}

func TestDriverRegistryReturnsDefensiveProbeSnapshot(t *testing.T) {
	driver := &stubDriver{
		info:  DriverInfo{ID: "gemini-web", Metadata: map[string]string{"source": "native"}},
		probe: ProbeResult{Status: DriverAvailable, Models: []ModelCapability{{ID: "gemini-web-auto", InputModalities: []string{"text"}}}},
	}
	registry := NewDriverRegistry()
	if err := registry.Register(driver); err != nil {
		t.Fatal(err)
	}
	snapshot := registry.ProbeAll(context.Background())[0]
	snapshot.Info.Metadata["source"] = "changed"
	snapshot.Probe.Models[0].InputModalities[0] = "changed"
	if driver.info.Metadata["source"] != "native" || driver.probe.Models[0].InputModalities[0] != "text" {
		t.Fatal("probe snapshot mutated driver-owned data")
	}
}
