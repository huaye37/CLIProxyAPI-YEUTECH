package websubscription

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestCatalogRejectsDuplicatesAndAmbiguity(t *testing.T) {
	_, err := NewToolCatalog([]ToolDefinition{{Type: ToolFunction, Name: "same"}, {Type: ToolFunction, Name: "same"}})
	if err == nil {
		t.Fatal("expected duplicate error")
	}

	catalog, err := NewToolCatalog([]ToolDefinition{
		{Type: ToolFunction, Namespace: "one", Name: "lookup"},
		{Type: ToolFunction, Namespace: "two", Name: "lookup"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.Resolve(ToolFunction, "", "lookup")
	if !errors.Is(err, ErrAmbiguousTool) {
		t.Fatalf("error = %v", err)
	}
}

func TestCatalogSpecificChoice(t *testing.T) {
	catalog, err := NewToolCatalog([]ToolDefinition{{Type: ToolFunction, Namespace: "mcp", Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateChoice(ToolChoice{Mode: ToolChoiceSpecific, Type: ToolFunction, Namespace: "mcp", Name: "lookup"}); err != nil {
		t.Fatal(err)
	}
}
