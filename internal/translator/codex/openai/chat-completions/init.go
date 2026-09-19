package chat_completions

import (
	"context"
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	// Generic Responses bridges share the Codex wire schema but return a plain
	// response object for non-streaming requests, rather than an SSE event.
	translator.Register(OpenAI, OpenaiResponse, ConvertOpenAIRequestToCodex,
		interfaces.TranslateResponse{
			Stream: ConvertCodexResponseToOpenAI,
			NonStream: func(ctx context.Context, model string, original, request, response []byte, param *any) []byte {
				wrapped := append([]byte(`{"type":"response.completed","response":`), response...)
				wrapped = append(wrapped, '}')
				return ConvertCodexResponseToOpenAINonStream(ctx, model, original, request, wrapped, param)
			},
		})
	translator.Register(
		OpenAI,
		Codex,
		ConvertOpenAIRequestToCodex,
		interfaces.TranslateResponse{
			Stream:    ConvertCodexResponseToOpenAI,
			NonStream: ConvertCodexResponseToOpenAINonStream,
		},
	)
}
