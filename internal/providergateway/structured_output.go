package providergateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	maximumStructuredSchemaBytes = 64 << 10
	maximumStructuredSchemaDepth = 64
	maximumStructuredSchemaNodes = 10000
)

func validateStructuredOutputSchema(raw json.RawMessage) error {
	if len(raw) < 2 || len(raw) > maximumStructuredSchemaBytes || !uniqueProviderJSON(raw) {
		return errors.New("invalid structured output schema")
	}
	document, err := decodeStructuredJSON(raw)
	if err != nil {
		return errors.New("invalid structured output schema")
	}
	if _, ok := document.(map[string]any); !ok || validateSchemaTree(document, 0, new(int)) != nil {
		return errors.New("unsupported structured output schema")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:engorch:structured-output", document); err != nil {
		return errors.New("invalid structured output schema")
	}
	if _, err := compiler.Compile("urn:engorch:structured-output"); err != nil {
		return errors.New("invalid structured output schema")
	}
	return nil
}

func validateSchemaTree(value any, depth int, nodes *int) error {
	*nodes++
	if depth > maximumStructuredSchemaDepth || *nodes > maximumStructuredSchemaNodes {
		return errors.New("structured output schema exceeds complexity bound")
	}
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			// External and local reference resolution are both excluded. This keeps
			// compilation self-contained and prevents ambient file/network loading.
			if key == "$ref" || key == "$dynamicRef" || key == "$recursiveRef" {
				return errors.New("structured output schema references are unavailable")
			}
			if err := validateSchemaTree(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := validateSchemaTree(child, depth+1, nodes); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateStructuredOutput(text string, required *RequiredCapabilities) error {
	if required == nil || required.StructuredOutput == StructuredOutputUnsupported || required.StructuredOutput == StructuredOutputTextParseRequired {
		return nil
	}
	value, err := decodeStructuredJSON([]byte(text))
	if err != nil {
		return errors.New("structured provider output is not exact JSON")
	}
	if required.StructuredOutput == StructuredOutputJSONOnly {
		return nil
	}
	if required.StructuredOutput != StructuredOutputStrictSchema || validateStructuredOutputSchema(required.Schema) != nil {
		return errors.New("invalid strict structured output contract")
	}
	schemaDocument, _ := decodeStructuredJSON(required.Schema)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:engorch:structured-output", schemaDocument); err != nil {
		return errors.New("invalid strict structured output contract")
	}
	schema, err := compiler.Compile("urn:engorch:structured-output")
	if err != nil || schema.Validate(value) != nil {
		return errors.New("provider output violates required JSON schema")
	}
	return nil
}

func validateStructuredOutputValue(raw json.RawMessage, schemaRaw json.RawMessage) error {
	if validateStructuredOutputSchema(schemaRaw) != nil {
		return errors.New("invalid terminal structured output schema")
	}
	value, err := decodeStructuredJSON(raw)
	if err != nil {
		return errors.New("terminal structured output is not exact JSON")
	}
	schemaDocument, err := decodeStructuredJSON(schemaRaw)
	if err != nil {
		return errors.New("invalid terminal structured output schema")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:engorch:terminal-structured-output", schemaDocument); err != nil {
		return errors.New("invalid terminal structured output schema")
	}
	schema, err := compiler.Compile("urn:engorch:terminal-structured-output")
	if err != nil || schema.Validate(value) != nil {
		return errors.New("terminal structured output violates schema")
	}
	return nil
}

// ValidateStructuredOutputValue validates one exact JSON object against the
// bounded, self-contained schema used by a native terminal result. It does not
// normalize or repair either input and performs no provider or filesystem I/O.
func ValidateStructuredOutputValue(raw json.RawMessage, schemaRaw json.RawMessage) error {
	return validateStructuredOutputValue(raw, schemaRaw)
}

func decodeStructuredJSON(raw []byte) (any, error) {
	if !uniqueProviderJSON(raw) {
		return nil, errors.New("ambiguous JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return value, nil
}

func structuredModeForTextFormat(format string) StructuredOutputMode {
	switch strings.TrimSpace(format) {
	case "json_object":
		return StructuredOutputJSONOnly
	case "json_schema":
		return StructuredOutputStrictSchema
	default:
		return StructuredOutputTextParseRequired
	}
}
