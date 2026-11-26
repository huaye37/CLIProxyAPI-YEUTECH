package pluginhost

import (
	"bytes"
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type funcRequestInterceptor struct {
	before func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error)
	after  func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error)
}

func (f funcRequestInterceptor) InterceptRequestBeforeAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if f.before != nil {
		return f.before(ctx, req)
	}
	return pluginapi.RequestInterceptResponse{}, nil
}

func (f funcRequestInterceptor) InterceptRequestAfterAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if f.after != nil {
		return f.after(ctx, req)
	}
	return pluginapi.RequestInterceptResponse{}, nil
}

type funcRequestNormalizer func(context.Context, pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error)

func (f funcRequestNormalizer) NormalizeRequest(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
	return f(ctx, req)
}

type funcResponseInterceptor func(context.Context, pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error)

func (f funcResponseInterceptor) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
	return f(ctx, req)
}

type funcStreamChunkInterceptor func(context.Context, pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error)

func (f funcStreamChunkInterceptor) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
	return f(ctx, req)
}

func TestPluginBodyLimitBypassesOversizedFullBodyHooks(t *testing.T) {
	t.Setenv(maxPluginBodyBytesEnv, "64")

	var routerCalls, fullBodyInterceptCalls, omitBodyInterceptCalls, normalizerCalls int
	var respSeenOrigLen, respSeenReqLen, streamSeenOrigLen, streamSeenReqLen int

	host := newRouteModelHostWithRecords(
		capabilityRecord{
			id:       "full-body-plugin",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "full-body-plugin"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					routerCalls++
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
				RequestInterceptor: funcRequestInterceptor{
					before: func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
						fullBodyInterceptCalls++
						return pluginapi.RequestInterceptResponse{}, nil
					},
				},
				RequestNormalizer: funcRequestNormalizer(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					normalizerCalls++
					return pluginapi.PayloadResponse{Body: []byte(`{"normalized":true}`)}, nil
				}),
				ResponseInterceptor: funcResponseInterceptor(func(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
					respSeenOrigLen = len(req.OriginalRequest)
					respSeenReqLen = len(req.RequestBody)
					return pluginapi.ResponseInterceptResponse{Body: req.Body}, nil
				}),
				StreamChunkInterceptor: funcStreamChunkInterceptor(func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
					streamSeenOrigLen = len(req.OriginalRequest)
					streamSeenReqLen = len(req.RequestBody)
					return pluginapi.StreamChunkInterceptResponse{Body: req.Body}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "omit-body-plugin",
			priority: 10,
			plugin: pluginapi.Plugin{
				SchemaVersion: pluginabi.SchemaVersionRequestInterceptorOmitBody,
				Capabilities: pluginapi.Capabilities{
					RequestInterceptorOmitBodyBefore: true,
					RequestInterceptor: funcRequestInterceptor{
						before: func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
							omitBodyInterceptCalls++
							return pluginapi.RequestInterceptResponse{}, nil
						},
					},
				},
			},
		},
	)

	ctx := context.Background()
	small := []byte(`{"small":true}`)
	large := bytes.Repeat([]byte("a"), 128)

	// 1. Small body: all hooks execute normally.
	if resp, ok := host.RouteModel(ctx, pluginapi.ModelRouteRequest{RequestedModel: "gemini-4-neon", Body: small}); !ok || resp.Target != "full-body-plugin" {
		t.Fatalf("small RouteModel = (%#v, %v), want handled by full-body-plugin", resp, ok)
	}
	host.InterceptRequestBeforeAuth(ctx, pluginapi.RequestInterceptRequest{Model: "gemini-4-neon", Body: small})
	gotSmallNorm := host.NormalizeRequest(ctx, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatAntigravity, "gemini-4-neon", small, true)
	if string(gotSmallNorm) != `{"normalized":true}` {
		t.Fatalf("small NormalizeRequest = %q, want normalized", gotSmallNorm)
	}
	host.InterceptResponse(ctx, pluginapi.ResponseInterceptRequest{OriginalRequest: small, RequestBody: small, Body: []byte(`{"ok":1}`)})
	host.InterceptStreamChunk(ctx, pluginapi.StreamChunkInterceptRequest{ChunkIndex: pluginapi.StreamChunkHeaderInitIndex, OriginalRequest: small, RequestBody: small, Body: []byte("data: 1")})
	if routerCalls != 1 || fullBodyInterceptCalls != 1 || omitBodyInterceptCalls != 1 || normalizerCalls != 1 {
		t.Fatalf("small counts = (router=%d full=%d omit=%d norm=%d), want (1,1,1,1)", routerCalls, fullBodyInterceptCalls, omitBodyInterceptCalls, normalizerCalls)
	}
	if respSeenOrigLen != len(small) || respSeenReqLen != len(small) || streamSeenOrigLen != len(small) || streamSeenReqLen != len(small) {
		t.Fatalf("small resp/stream request lens = (%d,%d,%d,%d), want all %d", respSeenOrigLen, respSeenReqLen, streamSeenOrigLen, streamSeenReqLen, len(small))
	}

	// 2. Oversized body: full-body hooks bypassed, omit-body still runs, response/stream interceptors run with nil request bodies.
	if resp, ok := host.RouteModel(ctx, pluginapi.ModelRouteRequest{RequestedModel: "gemini-4-neon", Body: large}); ok {
		t.Fatalf("large RouteModel = (%#v, %v), want bypass", resp, ok)
	}
	host.InterceptRequestBeforeAuth(ctx, pluginapi.RequestInterceptRequest{Model: "gemini-4-neon", Body: large})
	gotLargeNorm := host.NormalizeRequest(ctx, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatAntigravity, "gemini-4-neon", large, true)
	if !bytes.Equal(gotLargeNorm, large) {
		t.Fatalf("large NormalizeRequest mutated body; want bypass")
	}
	host.InterceptResponse(ctx, pluginapi.ResponseInterceptRequest{OriginalRequest: large, RequestBody: large, Body: []byte(`{"ok":2}`)})
	host.InterceptStreamChunk(ctx, pluginapi.StreamChunkInterceptRequest{ChunkIndex: pluginapi.StreamChunkHeaderInitIndex, OriginalRequest: large, RequestBody: large, Body: []byte("data: 2")})
	if routerCalls != 1 || fullBodyInterceptCalls != 1 || normalizerCalls != 1 {
		t.Fatalf("oversized full-body hooks should not run again: router=%d full=%d norm=%d", routerCalls, fullBodyInterceptCalls, normalizerCalls)
	}
	if omitBodyInterceptCalls != 2 {
		t.Fatalf("omit-body interceptor should still run on oversized request: calls=%d, want 2", omitBodyInterceptCalls)
	}
	if respSeenOrigLen != 0 || respSeenReqLen != 0 || streamSeenOrigLen != 0 || streamSeenReqLen != 0 {
		t.Fatalf("oversized resp/stream request lens = (%d,%d,%d,%d), want all 0", respSeenOrigLen, respSeenReqLen, streamSeenOrigLen, streamSeenReqLen)
	}

	// 3. Setting env to 0 disables the limit.
	t.Setenv(maxPluginBodyBytesEnv, "0")
	if host.MaxPluginBodyBytes() != 0 {
		t.Fatalf("MaxPluginBodyBytes() = %d, want 0", host.MaxPluginBodyBytes())
	}
	if resp, ok := host.RouteModel(ctx, pluginapi.ModelRouteRequest{RequestedModel: "gemini-4-neon", Body: large}); !ok || resp.Target != "full-body-plugin" {
		t.Fatalf("disabled-limit RouteModel = (%#v, %v), want handled", resp, ok)
	}
}
