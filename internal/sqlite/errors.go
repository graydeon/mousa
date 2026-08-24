package sqlite

import (
	"errors"
	"fmt"

	modernsqlite "modernc.org/sqlite"
)

// Code is a stable classification for storage failures.
type Code string

const (
	CodeInvalidRecord      Code = "invalid_record"
	CodeInvalidQuery       Code = "invalid_query"
	CodeResourceLimit      Code = "resource_limit"
	CodeNotFound           Code = "not_found"
	CodeConflict           Code = "conflict"
	CodeIncompatibleSchema Code = "incompatible_schema"
	CodeIntegrity          Code = "integrity"
	CodeReadOnly           Code = "read_only"
	CodeBusy               Code = "busy"
	CodeInternal           Code = "internal"
)

// Error preserves the underlying cause while exposing a stable code.
type Error struct {
	Code      Code
	Retryable bool
	Op        string
	Err       error
}

func (e *Error) Error() string {
	if e.Op == "" {
		return fmt.Sprintf("sqlite %s: %v", e.Code, e.Err)
	}
	return fmt.Sprintf("sqlite %s: %s: %v", e.Code, e.Op, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// IsCode reports whether err has the stable storage classification code.
func IsCode(err error, code Code) bool {
	var storageErr *Error
	return errors.As(err, &storageErr) && storageErr.Code == code
}

func wrap(code Code, op string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Retryable: code == CodeBusy, Op: op, Err: err}
}

func classify(op string, err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *modernsqlite.Error
	if !errors.As(err, &sqliteErr) {
		return wrap(CodeInternal, op, err)
	}
	switch sqliteErr.Code() & 0xff {
	case 5, 6:
		return wrap(CodeBusy, op, err)
	case 8:
		return wrap(CodeReadOnly, op, err)
	case 11, 26:
		return wrap(CodeIntegrity, op, err)
	case 13, 18:
		return wrap(CodeResourceLimit, op, err)
	case 19:
		return wrap(CodeIntegrity, op, err)
	default:
		return wrap(CodeInternal, op, err)
	}
}
