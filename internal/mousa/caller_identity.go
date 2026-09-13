package mousa

const (
	CallerSchema  = "mousa.caller.v1"
	PurposeSchema = "mousa.purpose.v1"
)

// Caller is one durable caller identity record. It carries no implied trust, no authentication
// state, and no lifecycle: it asserts only that this namespace and external caller ID name one
// identity.
type Caller struct {
	Schema           string   `json:"schema"`
	ID               CallerID `json:"id"`
	Namespace        string   `json:"namespace"`
	ExternalCallerID string   `json:"external_caller_id"`
}

// Purpose is one durable purpose identity record. Like Caller it carries no implied trust and no
// lifecycle: it asserts only that this namespace and external purpose ID name one purpose.
type Purpose struct {
	Schema            string    `json:"schema"`
	ID                PurposeID `json:"id"`
	Namespace         string    `json:"namespace"`
	ExternalPurposeID string    `json:"external_purpose_id"`
}

// Validate recomputes the identity and enforces the closed record vocabulary.
func (caller Caller) Validate() error {
	if caller.Schema != CallerSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.caller.v1", nil)
	}
	if caller.ID == (CallerID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if err := validateIdentityInput("namespace", caller.Namespace); err != nil {
		return err
	}
	if err := validateIdentityInput("external_caller_id", caller.ExternalCallerID); err != nil {
		return err
	}
	expected, err := NewCallerID(caller.Namespace, caller.ExternalCallerID)
	if err != nil {
		return err
	}
	if caller.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match namespace and external_caller_id", nil)
	}
	return nil
}

// Validate recomputes the identity and enforces the closed record vocabulary.
func (purpose Purpose) Validate() error {
	if purpose.Schema != PurposeSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.purpose.v1", nil)
	}
	if purpose.ID == (PurposeID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if err := validateIdentityInput("namespace", purpose.Namespace); err != nil {
		return err
	}
	if err := validateIdentityInput("external_purpose_id", purpose.ExternalPurposeID); err != nil {
		return err
	}
	expected, err := NewPurposeID(purpose.Namespace, purpose.ExternalPurposeID)
	if err != nil {
		return err
	}
	if purpose.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match namespace and external_purpose_id", nil)
	}
	return nil
}

// EncodeCaller returns the exact canonical caller bytes.
func EncodeCaller(caller Caller) ([]byte, error) {
	if err := caller.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(caller, "caller")
}

// DecodeCaller decodes one canonical caller payload and validates its identity.
func DecodeCaller(data []byte) (Caller, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Caller{}, err
	}
	var wire struct {
		Schema           string `json:"schema"`
		ID               string `json:"id"`
		Namespace        string `json:"namespace"`
		ExternalCallerID string `json:"external_caller_id"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return Caller{}, err
	}
	id, err := ParseCallerID(wire.ID)
	if err != nil {
		return Caller{}, validationErrorForField(err, "id")
	}
	caller := Caller{Schema: wire.Schema, ID: id, Namespace: wire.Namespace, ExternalCallerID: wire.ExternalCallerID}
	if err := caller.Validate(); err != nil {
		return Caller{}, err
	}
	return caller, nil
}

// EncodePurpose returns the exact canonical purpose bytes.
func EncodePurpose(purpose Purpose) ([]byte, error) {
	if err := purpose.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(purpose, "purpose")
}

func DecodePurpose(data []byte) (Purpose, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Purpose{}, err
	}
	var wire struct {
		Schema            string `json:"schema"`
		ID                string `json:"id"`
		Namespace         string `json:"namespace"`
		ExternalPurposeID string `json:"external_purpose_id"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return Purpose{}, err
	}
	id, err := ParsePurposeID(wire.ID)
	if err != nil {
		return Purpose{}, validationErrorForField(err, "id")
	}
	purpose := Purpose{Schema: wire.Schema, ID: id, Namespace: wire.Namespace, ExternalPurposeID: wire.ExternalPurposeID}
	if err := purpose.Validate(); err != nil {
		return Purpose{}, err
	}
	return purpose, nil
}
