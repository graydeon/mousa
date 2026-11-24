package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// The native service serves one owned store over loopback HTTP. Principals
// authenticate with a bearer token whose verifier (not the token) lives in the
// daemon configuration; read and write source grants are separate facts.

const (
	nativeMaxConfigBytes   = 64 << 10
	nativeMaxPrincipals    = 256
	nativeMaxGrantEntries  = 4096
	nativeMaxTokenBytes    = 512
	nativeMaxSmallBody     = 64 << 10
	nativeMaxDocumentBody  = 2 << 20
	nativeMaxQueryBudget   = 1 << 20
	nativeMaxInFlight      = 8
	nativeHandlerBound     = 30 * time.Second
	nativeShutdownBound    = 10 * time.Second
	nativeCitationsBound   = 128
	nativePrincipalIDShape = "principal IDs match [A-Za-z0-9][A-Za-z0-9._-]{0,63}"
)

type nativeConfig struct {
	Principals []nativePrincipalConfig `json:"principals"`
}

type nativePrincipalConfig struct {
	ID           string   `json:"id"`
	TokenSHA256  string   `json:"token_sha256"`
	ReadSources  []string `json:"read_sources"`
	WriteSources []string `json:"write_sources"`
	Operator     bool     `json:"operator,omitzero"`
}

type nativePrincipal struct {
	id       string
	verifier [32]byte
	read     map[mousa.SourceID]bool
	write    map[mousa.SourceID]bool
	operator bool
}

// loadNativeConfig reads the bounded daemon configuration and validates every
// principal. Configuration problems are reported without echoing values.
func loadNativeConfig(path string) ([]nativePrincipal, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open serve configuration")
	}
	defer file.Close()
	data, err := readBoundedInput(file, nativeMaxConfigBytes, "serve configuration")
	if err != nil {
		return nil, err
	}
	var config nativeConfig
	if err := decodeNative(data, &config); err != nil {
		return nil, errors.New("invalid serve configuration")
	}
	if len(config.Principals) == 0 || len(config.Principals) > nativeMaxPrincipals {
		return nil, errors.New("serve configuration needs 1 to 256 principals")
	}
	principals := make([]nativePrincipal, 0, len(config.Principals))
	ids := make(map[string]bool, len(config.Principals))
	verifiers := make(map[[32]byte]bool, len(config.Principals))
	for _, entry := range config.Principals {
		if !mousa.ValidPrincipalID(entry.ID) {
			return nil, fmt.Errorf("invalid principal id: %s", nativePrincipalIDShape)
		}
		if ids[entry.ID] {
			return nil, errors.New("serve configuration repeats a principal id")
		}
		ids[entry.ID] = true
		raw, err := hex.DecodeString(entry.TokenSHA256)
		if err != nil || len(raw) != sha256.Size || strings.ToLower(entry.TokenSHA256) != entry.TokenSHA256 {
			return nil, errors.New("principal token_sha256 must be 64 lowercase hex characters")
		}
		var verifier [32]byte
		copy(verifier[:], raw)
		if verifiers[verifier] {
			return nil, errors.New("serve configuration repeats a credential verifier")
		}
		verifiers[verifier] = true
		principal := nativePrincipal{
			id: entry.ID, verifier: verifier, operator: entry.Operator,
			read:  make(map[mousa.SourceID]bool, len(entry.ReadSources)),
			write: make(map[mousa.SourceID]bool, len(entry.WriteSources)),
		}
		if len(entry.ReadSources)+len(entry.WriteSources) > nativeMaxGrantEntries {
			return nil, errors.New("principal source grants exceed their bound")
		}
		for _, external := range entry.ReadSources {
			source, err := streamSource(external)
			if err != nil {
				return nil, errors.New("principal read grant is not a valid source identifier")
			}
			principal.read[source.ID] = true
		}
		for _, external := range entry.WriteSources {
			source, err := streamSource(external)
			if err != nil {
				return nil, errors.New("principal write grant is not a valid source identifier")
			}
			principal.write[source.ID] = true
		}
		principals = append(principals, principal)
	}
	return principals, nil
}

// decodeNative applies the shared request/config boundary rules: UTF-8, one
// object, known fields only, no trailing data.
func decodeNative[T any](data []byte, target *T) error {
	if !utf8.Valid(data) {
		return errors.New("input is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("input has trailing data")
	}
	return nil
}

type nativeService struct {
	store      *sqlite.Store
	principals []nativePrincipal
	slots      chan struct{}
	// unhealthy latches when an admission or terminal outcome could not be
	// persisted; /healthz reports it until restart resolves the coverage.
	unhealthy atomic.Bool
	// openMonitor serves the metadata-only monitor reads without a
	// credential. Request endpoints always authenticate.
	openMonitor bool
	hub         *stageHub
	// stageBoundary is a test-only hook invoked after each stage frame is
	// appended to the ring.
	stageBoundary func(stageFrame)
}

func newNativeService(store *sqlite.Store, principals []nativePrincipal) *nativeService {
	return &nativeService{store: store, principals: principals, slots: make(chan struct{}, nativeMaxInFlight), hub: newStageHub(stageRingSize)}
}

// publishStage appends one frame to the live ring and runs the test hook.
func (s *nativeService) publishStage(frame stageFrame) {
	s.hub.publish(frame)
	if s.stageBoundary != nil {
		s.stageBoundary(frame)
	}
}

// routes installs the outer checks every response shares — a server-generated
// request ID on the response header and a loopback Host — above method-aware
// routes.
func (s *nativeService) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("POST /v1/query", serveOperation(s, mousa.RequestOperationQuery, nativeMaxSmallBody, s.handleQuery))
	mux.HandleFunc("POST /v1/notes", serveOperation(s, mousa.RequestOperationNote, nativeMaxSmallBody, s.handleNote))
	mux.HandleFunc("POST /v1/documents", serveOperation(s, mousa.RequestOperationDocument, nativeMaxDocumentBody, s.handleDocument))
	mux.HandleFunc("GET /v1/requests", s.requestHistory)
	mux.HandleFunc("GET /v1/requests/{request_id}", s.requestDetail)
	mux.HandleFunc("GET /v1/events", s.stageEvents)
	installDashboard(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := rand.Text()
		w.Header().Set("X-Mousa-Request-ID", requestID)
		w.Header().Set("Cache-Control", "no-store")
		if !loopbackHost(r.Host) {
			_ = s.writeReply(w, http.StatusForbidden, requestID, nil, &nativeFailure{code: "forbidden"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.IsLoopback()
}

// authenticate resolves the bearer credential to a configured principal. Every
// credential failure is the same unauthorized reply.
func (s *nativeService) authenticate(r *http.Request) (*nativePrincipal, *nativeFailure) {
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		return nil, &nativeFailure{status: http.StatusUnauthorized, code: "unauthorized"}
	}
	token, ok := strings.CutPrefix(headers[0], "Bearer ")
	if !ok || token == "" || len(token) > nativeMaxTokenBytes || !utf8.ValidString(token) {
		return nil, &nativeFailure{status: http.StatusUnauthorized, code: "unauthorized"}
	}
	sum := sha256.Sum256([]byte(token))
	matched := -1
	for i := range s.principals {
		if subtle.ConstantTimeCompare(sum[:], s.principals[i].verifier[:]) == 1 {
			matched = i
		}
	}
	if matched < 0 {
		return nil, &nativeFailure{status: http.StatusUnauthorized, code: "unauthorized"}
	}
	return &s.principals[matched], nil
}

// nativeFailure is the scrubbed error a handler reports: an HTTP status and a
// stable code, never wrapped internals.
type nativeFailure struct {
	status int
	code   string
}

func (f *nativeFailure) Error() string { return f.code }

type nativeReply struct {
	RequestID string          `json:"request_id"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *nativeError    `json:"error,omitempty"`
}

type nativeError struct {
	Code string `json:"code"`
}

// nativeLabels carries the optional caller-reported client metadata block.
// The labels are stored verbatim and never marked verified.
type nativeLabels struct {
	Client mousa.ClientMetadata `json:"client,omitzero"`
}

func (labels nativeLabels) clientLabels() mousa.ClientMetadata { return labels.Client }

type nativeRequest interface {
	clientLabels() mousa.ClientMetadata
}

// serveOperation is the single entry point for served work. It persists an
// admission marker before any operation runs — unauthenticated, malformed,
// oversized, busy, and canceled attempts included — and guarantees one
// terminal outcome per admitted request. Admission and terminal writes run on
// a bounded detached context so a client disconnect still persists the
// attempt; when either cannot be recorded the service reports
// coverage_unavailable until restart.
func serveOperation[T nativeRequest](s *nativeService, operation mousa.RequestOperation, bodyLimit int64, handle func(context.Context, *nativePrincipal, mousa.RequestAdmission, *sqlite.Store, T) (any, mousa.RequestState, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		received := time.Now().UnixMicro()
		requestID := w.Header().Get("X-Mousa-Request-ID")
		if !mousa.ValidRequestID(requestID) {
			requestID = rand.Text()
			w.Header().Set("X-Mousa-Request-ID", requestID)
		}
		admission := mousa.RequestAdmission{
			RequestID:      requestID,
			Operation:      operation,
			ReceivedAtUsec: received,
		}
		// emit publishes one serve-side stage frame for this request.
		emit := func(stage mousa.Stage, phase mousa.StagePhase, durationUsec int64, state mousa.RequestState) {
			s.publishStage(stageFrame{
				RequestID: requestID, Operation: operation,
				Authenticated: admission.Authenticated, Principal: admission.Principal,
				Client: admission.Client, Stage: stage, Phase: phase,
				AtUsec: time.Now().UnixMicro(), DurationUsec: durationUsec, State: state,
			})
		}
		// begin opens a serve-side stage and returns its closer.
		begin := func(stage mousa.Stage) func(error) {
			start := time.Now()
			emit(stage, mousa.StagePhaseStart, 0, "")
			return func(err error) {
				phase := mousa.StagePhaseFinish
				if err != nil {
					phase = mousa.StagePhaseError
				}
				emit(stage, phase, time.Since(start).Microseconds(), "")
			}
		}
		finishReceive := begin(mousa.StageReceive)
		fail := func(status int, code string) {
			finishResponse := begin(mousa.StageResponse)
			finishResponse(s.writeReply(w, status, requestID, nil, &nativeFailure{code: code}))
		}
		detached := func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		}
		admitted := false
		admit := func(authenticated bool, principal string, client mousa.ClientMetadata) bool {
			admission.Authenticated = authenticated
			admission.Principal = principal
			admission.Client = client
			ctx, cancel := detached()
			defer cancel()
			finishQueue := begin(mousa.StageQueue)
			finishAdmission := begin(mousa.StageAdmission)
			err := s.store.AdmitRequest(ctx, admission)
			finishAdmission(err)
			finishQueue(err)
			if err != nil {
				s.unhealthy.Store(true)
				fail(http.StatusServiceUnavailable, "coverage_unavailable")
				return false
			}
			admitted = true
			return true
		}
		finish := func(state mousa.RequestState, code mousa.RequestErrorCode) bool {
			if !admitted {
				return false
			}
			ctx, cancel := detached()
			defer cancel()
			if err := s.store.FinishRequest(ctx, admission, state, code); err != nil {
				s.unhealthy.Store(true)
				fail(http.StatusServiceUnavailable, "coverage_unavailable")
				return false
			}
			emit(mousa.StageOutcome, mousa.StagePhaseTerminal, 0, state)
			return true
		}
		terminal := func(state mousa.RequestState, code mousa.RequestErrorCode, status int, reply string) {
			if finish(state, code) {
				fail(status, reply)
			}
		}
		// When the in-flight bound is full, authenticate only (no body read)
		// and record an evidence-free attempt.
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			principal, failure := s.authenticate(r)
			if failure != nil {
				finishReceive(errRefused)
				if !admit(false, "", mousa.ClientMetadata{}) {
					return
				}
				terminal(mousa.RequestStateDenied, mousa.RequestErrorUnauthorized, http.StatusUnauthorized, "unauthorized")
				return
			}
			finishReceive(nil)
			if !admit(true, principal.id, mousa.ClientMetadata{}) {
				return
			}
			terminal(mousa.RequestStateFailed, mousa.RequestErrorBusy, http.StatusServiceUnavailable, "busy")
			return
		}
		principal, failure := s.authenticate(r)
		if failure != nil {
			finishReceive(errRefused)
			if !admit(false, "", mousa.ClientMetadata{}) {
				return
			}
			terminal(mousa.RequestStateDenied, mousa.RequestErrorUnauthorized, http.StatusUnauthorized, "unauthorized")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			finishReceive(errRefused)
			if !admit(true, principal.id, mousa.ClientMetadata{}) {
				return
			}
			var tooLarge *http.MaxBytesError
			switch {
			case r.Context().Err() != nil:
				terminal(mousa.RequestStateCanceled, mousa.RequestErrorCanceled, http.StatusRequestTimeout, "canceled")
			case errors.As(err, &tooLarge):
				terminal(mousa.RequestStateInvalid, mousa.RequestErrorTooLarge, http.StatusRequestEntityTooLarge, "too_large")
			default:
				terminal(mousa.RequestStateInvalid, mousa.RequestErrorInvalidRequest, http.StatusBadRequest, "invalid_request")
			}
			return
		}
		var request T
		if err := decodeNative(body, &request); err != nil {
			finishReceive(errRefused)
			if !admit(true, principal.id, mousa.ClientMetadata{}) {
				return
			}
			terminal(mousa.RequestStateInvalid, mousa.RequestErrorInvalidRequest, http.StatusBadRequest, "invalid_request")
			return
		}
		client := request.clientLabels()
		if err := client.Validate(); err != nil {
			// Invalid reported labels are dropped, never stored or served.
			finishReceive(errRefused)
			if !admit(true, principal.id, mousa.ClientMetadata{}) {
				return
			}
			terminal(mousa.RequestStateInvalid, mousa.RequestErrorInvalidRequest, http.StatusBadRequest, "invalid_request")
			return
		}
		finishReceive(nil)
		if !admit(true, principal.id, client) {
			return
		}
		// A request canceled before or while it was read still records its
		// attempt on the detached context.
		if err := r.Context().Err(); err != nil {
			terminal(mousa.RequestStateCanceled, mousa.RequestErrorCanceled, http.StatusRequestTimeout, "canceled")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), nativeHandlerBound)
		defer cancel()
		// The observed store clone reports in-transaction stage boundaries
		// for this request only; the admission's own store stays unobserved.
		observed := s.store.Observed(func(observation mousa.StageObservation) {
			s.publishStage(stageFrame{
				RequestID: requestID, Operation: operation,
				Authenticated: admission.Authenticated, Principal: admission.Principal,
				Client: admission.Client, Stage: observation.Stage, Phase: observation.Phase,
				AtUsec: observation.AtUsec, DurationUsec: observation.DurationUsec, Attempt: observation.Attempt,
			})
		})
		result, state, err := handle(ctx, principal, admission, observed, request)
		if err != nil {
			outcomeState, code, status, public := classifyServeError(err)
			if !finish(outcomeState, code) {
				return
			}
			fail(status, public)
			return
		}
		// The core call already committed the outcome atomically; the frame
		// reports the durable state without re-reading the store.
		emit(mousa.StageOutcome, mousa.StagePhaseTerminal, 0, state)
		finishResponse := begin(mousa.StageResponse)
		finishResponse(s.writeReply(w, http.StatusOK, requestID, result, nil))
	}
}

// errRefused marks a receive stage that ended in refusal rather than service.
var errRefused = errors.New("request refused")

// classifyServeError maps an operation error to its stored outcome pairing
// and its public reply in one pass.
func classifyServeError(err error) (mousa.RequestState, mousa.RequestErrorCode, int, string) {
	var typed *nativeFailure
	if errors.As(err, &typed) {
		switch typed.code {
		case "unauthorized":
			return mousa.RequestStateDenied, mousa.RequestErrorUnauthorized, typed.status, "unauthorized"
		case "source_denied", "forbidden":
			return mousa.RequestStateDenied, mousa.RequestErrorSourceDenied, typed.status, typed.code
		case "invalid_request":
			return mousa.RequestStateInvalid, mousa.RequestErrorInvalidRequest, typed.status, "invalid_request"
		case "too_large":
			return mousa.RequestStateInvalid, mousa.RequestErrorTooLarge, typed.status, "too_large"
		case "conflict":
			return mousa.RequestStateFailed, mousa.RequestErrorConflict, typed.status, "conflict"
		case "busy":
			return mousa.RequestStateFailed, mousa.RequestErrorBusy, typed.status, "busy"
		case "canceled":
			return mousa.RequestStateCanceled, mousa.RequestErrorCanceled, typed.status, "canceled"
		}
		return mousa.RequestStateFailed, mousa.RequestErrorInternal, typed.status, typed.code
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return mousa.RequestStateCanceled, mousa.RequestErrorCanceled, http.StatusRequestTimeout, "canceled"
	case sqlite.IsCode(err, sqlite.CodeAccessDenied), sqlite.IsCode(err, sqlite.CodeNotFound):
		return mousa.RequestStateDenied, mousa.RequestErrorSourceDenied, http.StatusForbidden, "source_denied"
	case sqlite.IsCode(err, sqlite.CodeInvalidQuery), sqlite.IsCode(err, sqlite.CodeInvalidRecord):
		return mousa.RequestStateInvalid, mousa.RequestErrorInvalidRequest, http.StatusBadRequest, "invalid_request"
	case sqlite.IsCode(err, sqlite.CodeResourceLimit):
		return mousa.RequestStateInvalid, mousa.RequestErrorTooLarge, http.StatusRequestEntityTooLarge, "too_large"
	case sqlite.IsCode(err, sqlite.CodeConflict):
		return mousa.RequestStateFailed, mousa.RequestErrorConflict, http.StatusConflict, "conflict"
	case sqlite.IsCode(err, sqlite.CodeBusy):
		return mousa.RequestStateFailed, mousa.RequestErrorBusy, http.StatusServiceUnavailable, "busy"
	default:
		return mousa.RequestStateFailed, mousa.RequestErrorInternal, http.StatusInternalServerError, "internal"
	}
}

func (s *nativeService) writeReply(w http.ResponseWriter, status int, requestID string, result any, failure *nativeFailure) error {
	reply := nativeReply{RequestID: requestID}
	if failure != nil {
		reply.Error = &nativeError{Code: failure.code}
	} else if result != nil {
		data, err := json.Marshal(result)
		if err != nil {
			status, reply.Error = http.StatusInternalServerError, &nativeError{Code: "internal"}
		} else {
			reply.Result = data
		}
	}
	data, err := json.Marshal(reply)
	if err != nil {
		data = []byte(`{"request_id":"","error":{"code":"internal"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(data)
	return err
}

func (s *nativeService) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.unhealthy.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"unhealthy","code":"coverage_unavailable"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// operatorOnly enforces the operator credential on monitor endpoints. It
// writes the refusal reply itself; a false return means the reply is sent.
func (s *nativeService) operatorOnly(w http.ResponseWriter, r *http.Request) bool {
	if s.openMonitor {
		return true
	}
	requestID := w.Header().Get("X-Mousa-Request-ID")
	principal, failure := s.authenticate(r)
	if failure != nil {
		_ = s.writeReply(w, failure.status, requestID, nil, &nativeFailure{code: failure.code})
		return false
	}
	if !principal.operator {
		_ = s.writeReply(w, http.StatusForbidden, requestID, nil, &nativeFailure{code: "forbidden"})
		return false
	}
	return true
}

// requestHistory and requestDetail are operator-only monitor reads. They are
// excluded from the request audit loop and carry metadata only.
func (s *nativeService) requestHistory(w http.ResponseWriter, r *http.Request) {
	requestID := w.Header().Get("X-Mousa-Request-ID")
	fail := func(status int, code string) {
		_ = s.writeReply(w, status, requestID, nil, &nativeFailure{code: code})
	}
	if !s.operatorOnly(w, r) {
		return
	}
	var after uint64
	limit := 64
	query := r.URL.Query()
	if raw := query.Get("after"); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 63)
		if err != nil {
			fail(http.StatusBadRequest, "invalid_request")
			return
		}
		after = value
	}
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 16)
		if err != nil || value < 1 || value > 128 {
			fail(http.StatusBadRequest, "invalid_request")
			return
		}
		limit = int(value)
	}
	ctx, cancel := context.WithTimeout(r.Context(), nativeHandlerBound)
	defer cancel()
	// tail starts at the newest limit outcomes, so a viewer need not page
	// through the whole history to show recent requests.
	if query.Get("tail") != "" && query.Get("after") == "" {
		tail, err := s.store.RequestHistoryTail(ctx, limit)
		if err != nil {
			fail(http.StatusInternalServerError, "internal")
			return
		}
		after = tail
	}
	page, err := s.store.RequestHistory(ctx, after, limit)
	if err != nil {
		fail(http.StatusInternalServerError, "internal")
		return
	}
	_ = s.writeReply(w, http.StatusOK, requestID, page, nil)
}

func (s *nativeService) requestDetail(w http.ResponseWriter, r *http.Request) {
	requestID := w.Header().Get("X-Mousa-Request-ID")
	fail := func(status int, code string) {
		_ = s.writeReply(w, status, requestID, nil, &nativeFailure{code: code})
	}
	if !s.operatorOnly(w, r) {
		return
	}
	id := r.PathValue("request_id")
	if !mousa.ValidRequestID(id) {
		fail(http.StatusNotFound, "not_found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), nativeHandlerBound)
	defer cancel()
	detail, err := s.store.RequestDetail(ctx, id)
	if err != nil {
		if sqlite.IsCode(err, sqlite.CodeNotFound) {
			fail(http.StatusNotFound, "not_found")
			return
		}
		fail(http.StatusInternalServerError, "internal")
		return
	}
	if !s.openMonitor {
		_ = s.writeReply(w, http.StatusOK, requestID, detail, nil)
		return
	}
	// An open monitor shows viewers what was asked and what came back.
	content, err := s.store.RequestContent(ctx, id)
	if err != nil {
		fail(http.StatusInternalServerError, "internal")
		return
	}
	_ = s.writeReply(w, http.StatusOK, requestID, struct {
		sqlite.RequestHistoryDetail
		Content sqlite.RequestContent `json:"content"`
	}{detail, content}, nil)
}

// Request bodies are typed at the boundary. There is deliberately no caller,
// request_id, or grant field a client can spoof.
type nativeScope struct {
	Source     string   `json:"source,omitempty"`
	Sources    []string `json:"sources,omitempty"`
	Collection string   `json:"collection,omitempty"`
}

type nativeQuery struct {
	nativeLabels
	Scope       nativeScope `json:"scope"`
	Text        string      `json:"text"`
	BudgetBytes uint64      `json:"budget_bytes,omitzero"`
}

type nativeNote struct {
	nativeLabels
	Scope     nativeScope          `json:"scope"`
	Text      string               `json:"text"`
	Citations []mousa.NoteCitation `json:"citations,omitempty"`
}

type nativeDocument struct {
	nativeLabels
	Source        string          `json:"source"`
	Record        json.RawMessage `json:"record"`
	SegmentPolicy string          `json:"segment_policy,omitempty"`
}

type nativeDocumentResult struct {
	SourceID         mousa.SourceID          `json:"source_id"`
	ItemID           string                  `json:"item_id"`
	Action           string                  `json:"action"`
	RepresentationID *mousa.RepresentationID `json:"representation_id,omitempty"`
}

// scopeIDs translates external scope identifiers once at the boundary into
// typed store source IDs.
func scopeIDs(scope nativeScope) (mousa.Scope, error) {
	kinds := 0
	if scope.Source != "" {
		kinds++
	}
	if len(scope.Sources) != 0 {
		kinds++
	}
	if scope.Collection != "" {
		kinds++
	}
	if kinds != 1 {
		return mousa.Scope{}, errors.New("scope needs exactly one of source, sources, or collection")
	}
	translated := mousa.Scope{Collection: scope.Collection}
	resolve := func(external string) (mousa.SourceID, error) {
		source, err := streamSource(external)
		if err != nil {
			return mousa.SourceID{}, errors.New("scope source is not a valid identifier")
		}
		return source.ID, nil
	}
	switch {
	case scope.Source != "":
		id, err := resolve(scope.Source)
		if err != nil {
			return mousa.Scope{}, err
		}
		translated.Source = id
	case len(scope.Sources) != 0:
		if len(scope.Sources) > mousa.MaxScopeSources {
			return mousa.Scope{}, errors.New("scope lists too many sources")
		}
		seen := make(map[mousa.SourceID]bool, len(scope.Sources))
		for _, external := range scope.Sources {
			id, err := resolve(external)
			if err != nil {
				return mousa.Scope{}, err
			}
			if seen[id] {
				return mousa.Scope{}, errors.New("scope repeats a source")
			}
			seen[id] = true
			translated.Sources = append(translated.Sources, id)
		}
	case scope.Collection != "":
		if !mousa.ValidCollectionName(scope.Collection) {
			return mousa.Scope{}, errors.New("scope collection is not a valid name")
		}
	}
	return translated, nil
}

func (s *nativeService) handleQuery(ctx context.Context, principal *nativePrincipal, admission mousa.RequestAdmission, store *sqlite.Store, request nativeQuery) (any, mousa.RequestState, error) {
	scope, err := scopeIDs(request.Scope)
	if err != nil {
		return nil, "", &nativeFailure{status: http.StatusBadRequest, code: "invalid_request"}
	}
	if request.Text == "" || len(request.Text) > mcpMaxQueryBytes {
		return nil, "", &nativeFailure{status: http.StatusBadRequest, code: "invalid_request"}
	}
	budget := request.BudgetBytes
	if budget == 0 {
		budget = 8 << 10
	}
	if budget > nativeMaxQueryBudget {
		return nil, "", &nativeFailure{status: http.StatusBadRequest, code: "invalid_request"}
	}
	queried, err := store.QueryAdmitted(ctx, mousa.QueryRequest{
		Scope:            scope,
		Texts:            []string{request.Text},
		Mode:             mousa.QueryLexical,
		Budget:           mousa.Budget{Unit: mousa.BudgetBytes, Limit: budget},
		PermittedSources: principal.read,
	}, admission)
	if err != nil {
		return nil, "", err
	}
	state, _ := mousa.RequestTrailState(queried.Trail)
	return mcpQueryResult{Trail: queried.Trail.CallerView(), Passages: queried.Passages}, state, nil
}

func (s *nativeService) handleNote(ctx context.Context, principal *nativePrincipal, admission mousa.RequestAdmission, store *sqlite.Store, request nativeNote) (any, mousa.RequestState, error) {
	if request.Scope.Source == "" || len(request.Scope.Sources) != 0 || request.Scope.Collection != "" {
		return nil, "", &nativeFailure{status: http.StatusBadRequest, code: "invalid_request"}
	}
	if len(request.Text) == 0 || len(request.Text) > mousa.MaxUTF8TextSegmentBytes {
		return nil, "", &nativeFailure{status: http.StatusBadRequest, code: "invalid_request"}
	}
	if len(request.Citations) > nativeCitationsBound {
		return nil, "", &nativeFailure{status: http.StatusBadRequest, code: "invalid_request"}
	}
	source, err := streamSource(request.Scope.Source)
	if err != nil {
		return nil, "", &nativeFailure{status: http.StatusBadRequest, code: "invalid_request"}
	}
	if !principal.write[source.ID] {
		return nil, "", &nativeFailure{status: http.StatusForbidden, code: "source_denied"}
	}
	text := []byte(request.Text)
	note, created, err := store.RememberAdmitted(ctx, source.ID, text, request.Citations, principal.read, admission,
		func(scoped *sqlite.Store, itemID string, createdAtUsec int64) error {
			_, err := applyItem(ctx, scoped, source, itemInput{ID: itemID, Content: text, Captured: createdAtUsec}, nil, mousa.TextSegmentNoteV1)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	state := mousa.RequestStateNoOp
	if created {
		state = mousa.RequestStateCommitted
	}
	return note, state, nil
}

func (s *nativeService) handleDocument(ctx context.Context, principal *nativePrincipal, admission mousa.RequestAdmission, store *sqlite.Store, request nativeDocument) (any, mousa.RequestState, error) {
	if request.Source == "" || len(request.Record) == 0 || len(request.Record) > maxItemRecordBytes {
		return nil, "", &nativeFailure{status: http.StatusBadRequest, code: "invalid_request"}
	}
	policy := request.SegmentPolicy
	if policy == "" {
		policy = mousa.TextSegmentFixedV1
	}
	if policy != mousa.TextSegmentFixedV1 && policy != mousa.TextSegmentPassageV1 {
		return nil, "", &nativeFailure{status: http.StatusBadRequest, code: "invalid_request"}
	}
	source, err := streamSource(request.Source)
	if err != nil {
		return nil, "", &nativeFailure{status: http.StatusBadRequest, code: "invalid_request"}
	}
	if !principal.write[source.ID] {
		return nil, "", &nativeFailure{status: http.StatusForbidden, code: "source_denied"}
	}
	record, err := decodeItemRecord(request.Record)
	if err != nil {
		return nil, "", &nativeFailure{status: http.StatusBadRequest, code: "invalid_request"}
	}
	item := itemInput{ID: record.ID, Deleted: record.Deleted}
	if record.Text != nil {
		item.Content = []byte(*record.Text)
	}
	result := nativeDocumentResult{SourceID: source.ID, ItemID: record.ID}
	changed := false
	err = store.ApplyRequestDocument(ctx, admission, source.ID, record.ID, func(scoped *sqlite.Store) (bool, error) {
		action, err := applyItem(ctx, scoped, source, item, nil, policy)
		if err != nil {
			return false, err
		}
		result.Action = action
		current, err := scoped.GetLocalItem(ctx, source.ID, record.ID)
		switch {
		case err == nil:
			result.RepresentationID = &current.RepresentationID
		case sqlite.IsCode(err, sqlite.CodeNotFound):
		default:
			return false, err
		}
		switch action {
		case "added", "updated", "restored", "deleted":
			changed = true
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, "", err
	}
	state := mousa.RequestStateNoOp
	if changed {
		state = mousa.RequestStateCommitted
	}
	return result, state, nil
}

// serveCommand owns one existing store and serves it on a loopback listener
// until the process is interrupted.
func serveCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("serve")
	listen := flags.String("listen", "127.0.0.1:8787", "loopback host:port to bind; 0 selects a port")
	configPath := flags.String("config", "", "principal configuration file (required)")
	openMonitor := flags.Bool("open-monitor", false, "serve the dashboard, stage stream and each request's text and returned passages without a credential")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if *configPath == "" || len(flags.Args()) != 0 {
		return usageError{"serve requires --config <file> and no positional arguments"}
	}
	listenAddr, err := loopbackListenAddr(*listen)
	if err != nil {
		return usageError{err.Error()}
	}
	principals, err := loadNativeConfig(*configPath)
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(storePath)
	if err != nil {
		return fmt.Errorf("serve requires an existing store: %w", err)
	}
	lock, err := lockStoreFile(canonical)
	if err != nil {
		return err
	}
	defer lock.Close()
	store, err := sqlite.Open(ctx, canonical)
	if err != nil {
		return err
	}
	// Resolve admissions an interrupted run left behind before announcing
	// the listener; a parity disagreement refuses, never heals.
	if err := store.RecoverRequests(ctx); err != nil {
		return errors.Join(err, store.Close())
	}
	service := newNativeService(store, principals)
	service.openMonitor = *openMonitor
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return errors.Join(err, store.Close())
	}
	server := &http.Server{
		Handler: service.routes(),
		// Cancel pending request contexts on shutdown so handlers abort;
		// detached terminal writes still persist their outcomes.
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	// The bound address is announced only after the store is owned and open.
	fmt.Println("http://" + listener.Addr().String())
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	var serveErr error
	select {
	case serveErr = <-finished:
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), nativeShutdownBound)
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		cancel()
		serveErr = <-finished
	}
	closeErr := store.Close()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, closeErr)
}

// loopbackListenAddr validates a numeric loopback host:port; the port may be 0
// so tests bind an ephemeral address.
func loopbackListenAddr(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", errors.New("listen must be host:port")
	}
	address, err := netip.ParseAddr(host)
	if err != nil || !address.IsLoopback() {
		return "", errors.New("listen host must be a loopback IP literal")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return "", errors.New("listen port must be numeric")
	}
	return net.JoinHostPort(host, strconv.FormatUint(portNumber, 10)), nil
}
