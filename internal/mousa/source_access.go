package mousa

// SourceAccess is the state an access event sets for a source. A source with no access event
// is denied.
type SourceAccess string

const (
	SourceAccessAllow SourceAccess = "allow"
	SourceAccessDeny  SourceAccess = "deny"
)

// Validate rejects any value outside the closed access vocabulary.
func (access SourceAccess) Validate() error {
	if access != SourceAccessAllow && access != SourceAccessDeny {
		return newValidationError("access", ValidationCodeInvalidEnum, "must be allow or deny", nil)
	}
	return nil
}
