package pluginhost

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRouteModelExceptPreventsMutualRouterPingPong(t *testing.T) {
	var routerACalls, routerBCalls int
	host := newRouteModelHostWithRecords(
		capabilityRecord{
			id:       "router-a",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "router-a"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					routerACalls++
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "router-b",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "router-b"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					routerBCalls++
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
	)

	// Hop 1: initial request enters router-a.
	ctx := context.Background()
	resp1, ok1 := host.RouteModelExcept(ctx, pluginapi.ModelRouteRequest{RequestedModel: "gemini-4-neon-xhigh"}, "")
	if !ok1 || resp1.Target != "router-a" {
		t.Fatalf("hop 1 = (%#v, %v), want router-a", resp1, ok1)
	}

	// Hop 2: router-a invokes host.model.execute* -> nested context records router-a, so router-b handles it.
	ctx2, err := withNestedHostModelCallback(ctx, "router-a")
	if err != nil {
		t.Fatalf("hop 2 nested ctx error = %v", err)
	}
	resp2, ok2 := host.RouteModelExcept(ctx2, pluginapi.ModelRouteRequest{RequestedModel: "gemini-4-neon-xhigh"}, "router-a")
	if !ok2 || resp2.Target != "router-b" {
		t.Fatalf("hop 2 = (%#v, %v), want router-b", resp2, ok2)
	}

	// Hop 3: router-b invokes host.model.execute* -> nested context contains BOTH router-a and router-b.
	// Without visited-set tracking, skipPluginID="router-b" would overwrite "router-a" and loop infinitely.
	ctx3, err := withNestedHostModelCallback(ctx2, "router-b")
	if err != nil {
		t.Fatalf("hop 3 nested ctx error = %v", err)
	}
	resp3, ok3 := host.RouteModelExcept(ctx3, pluginapi.ModelRouteRequest{RequestedModel: "gemini-4-neon-xhigh"}, "router-b")
	if ok3 {
		t.Fatalf("hop 3 re-entered router (%#v); expected fall-through to native upstream", resp3)
	}
	if routerACalls != 1 || routerBCalls != 1 {
		t.Fatalf("router calls = (a=%d, b=%d), want (1, 1)", routerACalls, routerBCalls)
	}
}

func TestNestedHostModelCallbackDepthLimit(t *testing.T) {
	ctx := context.Background()
	var err error
	for i := 1; i <= maxHostModelCallbackDepth; i++ {
		ctx, err = withNestedHostModelCallback(ctx, "plugin")
		if err != nil {
			t.Fatalf("depth %d unexpected error = %v", i, err)
		}
	}
	if _, err = withNestedHostModelCallback(ctx, "plugin"); err == nil || !strings.Contains(err.Error(), "nesting depth exceeded") {
		t.Fatalf("expected nesting depth exceeded error, got %v", err)
	}
}
