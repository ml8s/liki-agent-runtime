package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func decodeAgentDeployment(raw []byte) (*AgentDeployment, error) {
	var deployment AgentDeployment
	if err := decodeStrictJSON(raw, &deployment); err != nil {
		return nil, fmt.Errorf("decode agent deployment: %w", err)
	}
	if deployment.SchemaVersion != ContractVersion {
		return nil, fmt.Errorf("unsupported schema_version %q; want %q", deployment.SchemaVersion, ContractVersion)
	}
	if err := deployment.validate(); err != nil {
		return nil, err
	}
	return &deployment, nil
}

// decodeStrictJSON rejects duplicate object keys, trailing JSON values, and
// unknown typed fields. Artifact digests must not make ambiguous JSON portable.
func decodeStrictJSON(raw []byte, target any) error {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON document: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON document contains trailing values")
		}
		return fmt.Errorf("decode JSON trailing input: %w", err)
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := rejectDuplicateKeysInValue(decoder, "$"); err != nil {
		return fmt.Errorf("JSON object key conflict: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON input: %w", err)
	}
	return nil
}

func rejectDuplicateKeysInValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read JSON token: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("read object key: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("expected object key at %s", path)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate key %q at %s", key, path)
			}
			seen[key] = struct{}{}
			if err := rejectDuplicateKeysInValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		index := 0
		for decoder.More() {
			if err := rejectDuplicateKeysInValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
			index++
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected delimiter %q", delim)
	}
}
