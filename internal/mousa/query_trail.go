package mousa

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	TrailSchema                   = "mousa.trail.v1"
	MaxQueryCandidates            = 100
	MaxScopeSources               = 128
	ScopedBM25                    = "fts5-bm25-ancestry-shard-v1"
	QueryLexical       QueryMode  = "lexical"
	BudgetBytes        BudgetUnit = "bytes"
)

type QueryMode string
type BudgetUnit string

type QueryRequest struct {
	Scope             Scope
	Texts             []string
	Mode              QueryMode
	Budget            Budget
	Limit             int
	Caller            string
	Purpose           string
	RequestID         string
	IncludeSuperseded bool
	// Nil is the store owner. A caller grant must cover the entire scope.
	PermittedSources map[SourceID]bool
}

type Scope struct {
	Source     SourceID
	Sources    []SourceID
	Collection string
}

type Budget struct {
	Limit uint64     `json:"limit"`
	Unit  BudgetUnit `json:"unit"`
}

type QueryResult struct {
	Trail    Trail     `json:"trail"`
	Passages []Passage `json:"passages"`
}

type Passage struct {
	SourceID             SourceID         `json:"source_id"`
	Item                 string           `json:"item"`
	SegmentID            SegmentID        `json:"segment_id"`
	RepresentationID     RepresentationID `json:"representation_id"`
	RepresentationSHA256 SHA256           `json:"representation_sha256"`
	SegmentPolicy        string           `json:"segment_policy"`
	Rank                 int              `json:"rank"`
	Range                ByteRange        `json:"range"`
	Extract              bool             `json:"extract"`
	ContentSHA256        SHA256           `json:"content_sha256"`
	Text                 string           `json:"text"`
	Path                 string           `json:"path"`
	Note                 *NoteEvidence    `json:"note,omitempty"`
}

// Trail and its nested wire types declare fields in canonical key order.
type Trail struct {
	AsOf         uint64               `json:"as_of"`
	Candidates   []Candidate          `json:"candidates,omitzero"`
	ID           SourceTrailID        `json:"id,omitzero"`
	Packing      *Packing             `json:"packing,omitempty"`
	Request      TrailRequest         `json:"request"`
	ResultSHA256 SHA256               `json:"result_sha256"`
	Retrieval    *Retrieval           `json:"retrieval,omitempty"`
	Schema       string               `json:"schema"`
	Sources      []SourceDecision     `json:"sources"`
	StoreID      string               `json:"store_id"`
	Supersession []SupersessionEffect `json:"supersession,omitempty"`
}

type TrailRequest struct {
	Budget            Budget     `json:"budget"`
	Caller            string     `json:"caller,omitempty"`
	IncludeSuperseded bool       `json:"include_superseded,omitempty"`
	Limit             int        `json:"limit"`
	Mode              QueryMode  `json:"mode"`
	Purpose           string     `json:"purpose,omitempty"`
	RequestID         string     `json:"request_id,omitempty"`
	RequestedAtUsec   uint64     `json:"requested_at_usec,omitzero"`
	Scope             TrailScope `json:"scope"`
	Texts             []string   `json:"texts"`
}

// SupersessionEffect records that one candidate note was already replaced by
// newer durable notes at the trail's snapshot. The note ID names the note
// that owns the candidate's segment; each entry of SupersededBy is a
// replacement note ID.
type SupersessionEffect struct {
	NoteID       SHA256    `json:"note_id"`
	SegmentID    SegmentID `json:"segment_id"`
	SupersededBy []SHA256  `json:"superseded_by"`
}

type TrailScope struct {
	Kind    string     `json:"kind"`
	Name    string     `json:"name,omitempty"`
	Sources []SourceID `json:"sources"`
}

type SourceDecision struct {
	Access        SourceAccess   `json:"access,omitempty"`
	AccessEventID *AccessEventID `json:"access_event_id,omitempty"`
	Head          *SourceHead    `json:"head,omitempty"`
	Outcome       string         `json:"outcome"`
	Reasons       []string       `json:"reasons,omitempty"`
	SourceID      SourceID       `json:"source_id"`
	WithdrawalID  *WithdrawalID  `json:"withdrawal_id,omitempty"`
}

type SourceHead struct {
	LeafSHA256 SHA256 `json:"leaf_sha256"`
	Seq        uint64 `json:"seq"`
}

type Retrieval struct {
	Branches []Branch `json:"branches"`
	Fusion   string   `json:"fusion,omitempty"`
}

type Branch struct {
	DroppedNotAllowed int        `json:"dropped_not_allowed"`
	DroppedUnknown    int        `json:"dropped_unknown"`
	Kind              string     `json:"kind"`
	Proposed          int        `json:"proposed"`
	Scoring           string     `json:"scoring"`
	SharedBM25        *BM25Stats `json:"shared_bm25,omitempty"`
	TermPolicy        string     `json:"term_policy"`
	Terms             []string   `json:"terms"`
	Text              int        `json:"text"`
}

type Candidate struct {
	ContentSHA256    SHA256           `json:"content_sha256"`
	Cost             uint64           `json:"cost"`
	DuplicateOf      SegmentID        `json:"duplicate_of,omitzero"`
	Extracts         []TrailExtract   `json:"extracts,omitempty"`
	Omitted          string           `json:"omitted,omitempty"`
	Packed           string           `json:"packed"`
	Range            ByteRange        `json:"range"`
	Rank             int              `json:"rank"`
	Rejected         []string         `json:"rejected,omitempty"`
	RepresentationID RepresentationID `json:"representation_id"`
	Scores           []TrailScore     `json:"scores"`
	SegmentID        SegmentID        `json:"segment_id"`
	SourceIDs        []SourceID       `json:"source_ids"`
}

type ByteRange struct {
	End   uint64 `json:"end"`
	Start uint64 `json:"start"`
}

type TrailExtract struct {
	Range  ByteRange `json:"range"`
	SHA256 SHA256    `json:"sha256"`
}

type TrailScore struct {
	BM25      *BM25Match `json:"bm25,omitempty"`
	Branch    int        `json:"branch"`
	Level     string     `json:"level"`
	Rank      int        `json:"rank"`
	ValueBits string     `json:"value_bits"`
}

type Packing struct {
	Policy string `json:"policy"`
	Used   uint64 `json:"used"`
}

// CallerTrail deliberately cannot represent global log positions, denied
// decision details, or counts of proposals dropped by the verifier.
type CallerTrail struct {
	Candidates   *[]Candidate         `json:"candidates,omitempty"`
	ID           SourceTrailID        `json:"id"`
	Packing      *Packing             `json:"packing,omitempty"`
	Request      TrailRequest         `json:"request"`
	ResultSHA256 SHA256               `json:"result_sha256"`
	Retrieval    *CallerRetrieval     `json:"retrieval,omitempty"`
	Schema       string               `json:"schema"`
	Sources      []CallerSource       `json:"sources"`
	StoreID      string               `json:"store_id"`
	Supersession []SupersessionEffect `json:"supersession,omitempty"`
}

type CallerSource struct {
	Access        SourceAccess   `json:"access,omitempty"`
	AccessEventID *AccessEventID `json:"access_event_id,omitempty"`
	Head          *CallerHead    `json:"head,omitempty"`
	Outcome       string         `json:"outcome"`
	Reasons       []string       `json:"reasons,omitempty"`
	SourceID      SourceID       `json:"source_id"`
}

type CallerHead struct {
	LeafSHA256 SHA256 `json:"leaf_sha256"`
}

type CallerRetrieval struct {
	Branches []CallerBranch `json:"branches"`
	Fusion   string         `json:"fusion,omitempty"`
}

type CallerBranch struct {
	Kind       string   `json:"kind"`
	Proposed   int      `json:"proposed"`
	Scoring    string   `json:"scoring"`
	TermPolicy string   `json:"term_policy"`
	Terms      []string `json:"terms"`
	Text       int      `json:"text"`
}

func (trail Trail) CallerView() CallerTrail {
	view := CallerTrail{
		ID: trail.ID, Packing: trail.Packing, Request: trail.Request, ResultSHA256: trail.ResultSHA256,
		Schema: trail.Schema, Sources: make([]CallerSource, 0, len(trail.Sources)), StoreID: trail.StoreID,
		Supersession: trail.Supersession,
	}
	if trail.Retrieval != nil {
		view.Candidates = &trail.Candidates
		view.Retrieval = &CallerRetrieval{Fusion: trail.Retrieval.Fusion}
		for _, branch := range trail.Retrieval.Branches {
			view.Retrieval.Branches = append(view.Retrieval.Branches, CallerBranch{
				Kind: branch.Kind, Proposed: branch.Proposed, Scoring: branch.Scoring,
				TermPolicy: branch.TermPolicy, Terms: branch.Terms, Text: branch.Text,
			})
		}
	}
	for _, source := range trail.Sources {
		decision := CallerSource{Outcome: source.Outcome, SourceID: source.SourceID}
		if source.Outcome == "allow" {
			decision.Access = source.Access
			decision.AccessEventID = source.AccessEventID
			decision.Head = &CallerHead{LeafSHA256: source.Head.LeafSHA256}
			decision.Reasons = source.Reasons
		}
		view.Sources = append(view.Sources, decision)
	}
	return view
}

// NewTrail validates the builder's inputs and derives both trail identities.
func NewTrail(trail Trail) (Trail, error) {
	trail.Schema = TrailSchema
	if err := trail.validate(); err != nil {
		return Trail{}, err
	}
	digest, err := trail.resultDigest()
	if err != nil {
		return Trail{}, err
	}
	trail.ResultSHA256 = digest
	trail.ID = SourceTrailID{}
	data, err := canonicalTrailJSON(trail)
	if err != nil {
		return Trail{}, err
	}
	trail.ID = SourceTrailID(sha256.Sum256(data))
	return trail, nil
}

func EncodeTrail(trail Trail) ([]byte, error) {
	checked, err := NewTrail(trail)
	if err != nil {
		return nil, err
	}
	if trail.Schema != checked.Schema || trail.ID != checked.ID || trail.ResultSHA256 != checked.ResultSHA256 {
		return nil, errors.New("trail schema or identity disagrees with record")
	}
	return canonicalTrailJSON(trail)
}

func DecodeTrail(data []byte) (Trail, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Trail{}, err
	}
	var trail Trail
	if err := decodeJSONContract(data, &trail); err != nil {
		return Trail{}, err
	}
	canonical, err := EncodeTrail(trail)
	if err != nil {
		return Trail{}, err
	}
	if !bytes.Equal(canonical, data) {
		return Trail{}, errors.New("trail bytes are not canonical")
	}
	return trail, nil
}

func canonicalTrailJSON(value any) ([]byte, error) {
	data, err := encodeJSONContract(value, "trail")
	return bytes.TrimSuffix(data, []byte("\n")), err
}

func (trail Trail) resultDigest() (SHA256, error) {
	request := trail.Request
	request.Caller, request.Purpose, request.RequestID, request.RequestedAtUsec = "", "", "", 0
	sources := slices.Clone(trail.Sources)
	for i, source := range sources {
		if source.Outcome == "deny" {
			sources[i] = SourceDecision{Outcome: source.Outcome, SourceID: source.SourceID}
		}
	}
	data, err := canonicalTrailJSON(struct {
		Candidates   []Candidate          `json:"candidates,omitzero"`
		Packing      *Packing             `json:"packing,omitempty"`
		Request      TrailRequest         `json:"request"`
		Retrieval    *Retrieval           `json:"retrieval,omitempty"`
		Schema       string               `json:"schema"`
		Sources      []SourceDecision     `json:"sources"`
		Supersession []SupersessionEffect `json:"supersession,omitempty"`
	}{trail.Candidates, trail.Packing, request, trail.Retrieval, trail.Schema, sources, trail.Supersession})
	return SHA256(sha256.Sum256(data)), err
}

func (request TrailRequest) Validate() error {
	if request.Mode != QueryLexical || request.Budget.Unit != BudgetBytes || request.Budget.Limit == 0 ||
		request.Budget.Limit > maxJSONSafeInteger || request.Limit < 1 || request.Limit > MaxQueryCandidates ||
		request.RequestedAtUsec == 0 || request.RequestedAtUsec > maxJSONSafeInteger {
		return errors.New("invalid trail request parameters")
	}
	if request.Caller == "" || request.RequestID == "" || !trailText(request.Caller) ||
		!trailText(request.Purpose) || !trailText(request.RequestID) || len(request.Texts) < 1 || len(request.Texts) > 4 {
		return errors.New("invalid trail request metadata")
	}
	for i, text := range request.Texts {
		if len(text) == 0 || len(text) > 4096 || !trailText(text) || slices.Contains(request.Texts[:i], text) {
			return errors.New("query texts must be distinct, valid canonical strings of 1 to 4096 bytes")
		}
	}
	scope := request.Scope
	valid := false
	switch scope.Kind {
	case "source":
		valid = scope.Name == "" && len(scope.Sources) == 1
	case "list":
		valid = scope.Name == "" && len(scope.Sources) != 0
	case "collection":
		valid = ValidCollectionName(scope.Name) && scope.Sources != nil
	}
	if !valid || len(scope.Sources) > MaxScopeSources || !sortedSourceIDs(scope.Sources) {
		return errors.New("invalid trail scope")
	}
	return nil
}

func trailText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsAny(value, "\u2028\u2029")
}

func sortedSourceIDs(ids []SourceID) bool {
	for i, id := range ids {
		if id == (SourceID{}) || (i > 0 && bytes.Compare(ids[i-1][:], id[:]) >= 0) {
			return false
		}
	}
	return true
}

// DecideSource combines the source's lifecycle reason, if any, with its access setting.
func DecideSource(access SourceAccess, lifecycle string) (string, []string) {
	if lifecycle == "unknown_source" {
		return "deny", []string{lifecycle}
	}
	var reasons []string
	if lifecycle != "" {
		reasons = append(reasons, lifecycle)
	}
	switch access {
	case "default":
		reasons = append(reasons, "access_default")
	case SourceAccessDeny:
		reasons = append(reasons, "access_denied")
	}
	if len(reasons) != 0 {
		return "deny", reasons
	}
	return "allow", []string{"allow"}
}

func (trail Trail) validate() error {
	storeID, err := hex.DecodeString(trail.StoreID)
	if err != nil || len(storeID) != 16 || strings.ToLower(trail.StoreID) != trail.StoreID || trail.AsOf > maxJSONSafeInteger {
		return errors.New("invalid trail store identity or snapshot index")
	}
	if err := trail.Request.Validate(); err != nil {
		return err
	}
	if trail.Sources == nil || len(trail.Sources) != len(trail.Request.Scope.Sources) {
		return errors.New("trail decisions disagree with scope")
	}
	allowed := make(map[SourceID]bool, len(trail.Sources))
	for i, source := range trail.Sources {
		if source.SourceID != trail.Request.Scope.Sources[i] {
			return errors.New("trail decision is out of scope or order")
		}
		if err := source.validate(trail.AsOf); err != nil {
			return fmt.Errorf("trail decision %s: %w", source.SourceID, err)
		}
		if source.Outcome == "allow" {
			allowed[source.SourceID] = true
		}
	}
	if len(allowed) == 0 {
		if trail.Retrieval != nil || trail.Candidates != nil || trail.Packing != nil || len(trail.Supersession) != 0 {
			return errors.New("all-denied trail cannot contain retrieval, candidates, packing or supersession")
		}
		return nil
	}
	if trail.Retrieval == nil || trail.Candidates == nil || trail.Packing == nil {
		return errors.New("allowed trail requires retrieval, candidates and packing")
	}
	return trail.validateRetrieval(allowed)
}

func (source SourceDecision) validate(asOf uint64) error {
	lifecycle := ""
	switch {
	case slices.Contains(source.Reasons, "unknown_source"):
		if source.Access != "default" || source.WithdrawalID != nil {
			return errors.New("unknown source cannot have access history or withdrawal")
		}
		lifecycle = "unknown_source"
	case source.WithdrawalID != nil:
		if *source.WithdrawalID == (WithdrawalID{}) {
			return errors.New("invalid withdrawal ID")
		}
		lifecycle = "source_withdrawn"
	case slices.Contains(source.Reasons, "missing_source_state"):
		lifecycle = "missing_source_state"
	}
	if source.Access != "default" && source.Access != SourceAccessAllow && source.Access != SourceAccessDeny {
		return errors.New("unknown access state")
	}
	if (source.Access == "default") != (source.AccessEventID == nil) ||
		(source.AccessEventID != nil && *source.AccessEventID == (AccessEventID{})) {
		return errors.New("access event disagrees with access state")
	}
	outcome, reasons := DecideSource(source.Access, lifecycle)
	if outcome != source.Outcome || !slices.Equal(source.Reasons, reasons) {
		return errors.New("outcome and reasons disagree with source decision")
	}
	if outcome == "deny" {
		if source.Head != nil {
			return errors.New("denied source cannot disclose a head")
		}
	} else if source.Head == nil || source.Head.Seq == 0 || source.Head.Seq > asOf || source.Head.LeafSHA256 == (SHA256{}) {
		return errors.New("allowed source requires a head at or before as_of")
	}
	return nil
}

func (trail Trail) validateRetrieval(allowed map[SourceID]bool) error {
	branches := trail.Retrieval.Branches
	if len(branches) != len(trail.Request.Texts) {
		return errors.New("retrieval branches or fusion disagree with query texts")
	}
	shared := branches[0].Scoring == SharedBM25
	if shared && len(allowed) < 2 {
		return errors.New("shared BM25 requires multiple allowed sources")
	}
	fusion := ""
	if len(branches) > 1 {
		fusion = "rrf-k60-v1"
	}
	if shared {
		fusion = SharedBM25
		if len(branches) > 1 {
			fusion = SharedBM25RRF
		}
	}
	if trail.Retrieval.Fusion != fusion {
		return errors.New("retrieval branches or fusion disagree with query texts")
	}
	var proposed [4]int
	var idfs [4][]float64
	for i, branch := range branches {
		if branch.Kind != "lexical" || branch.Text != i || (branch.Scoring != "fts5-bm25" && branch.Scoring != ScopedBM25 && branch.Scoring != SharedBM25) ||
			(branch.Scoring == SharedBM25) != shared || (branch.Scoring == SharedBM25) != (branch.SharedBM25 != nil) ||
			(branch.TermPolicy != "original" && branch.TermPolicy != "dedup") || branch.Terms == nil ||
			branch.Proposed < 0 || branch.Proposed > trail.Request.Limit || branch.DroppedUnknown < 0 ||
			branch.DroppedNotAllowed < 0 || branch.DroppedUnknown > branch.Proposed ||
			branch.DroppedNotAllowed > branch.Proposed-branch.DroppedUnknown {
			return errors.New("invalid lexical branch")
		}
		if shared {
			if len(branch.SharedBM25.Frequencies) != len(branch.Terms) {
				return errors.New("invalid shared BM25 statistics")
			}
			idf, err := branch.SharedBM25.IDF()
			if err != nil {
				return errors.New("invalid shared BM25 statistics")
			}
			idfs[i] = idf
		}
		for _, term := range branch.Terms {
			if term == "" || !trailText(term) {
				return errors.New("invalid lexical term")
			}
		}
		proposed[i] = branch.Proposed - branch.DroppedUnknown - branch.DroppedNotAllowed
	}
	check := slices.Clone(trail.Candidates)
	// seen doubles as the supersession eligibility set: an unrejected
	// candidate may carry an effect, a rejected one may not.
	seen := make(map[SegmentID]bool, len(check))
	for i, candidate := range check {
		if _, duplicate := seen[candidate.SegmentID]; duplicate || candidate.SegmentID == (SegmentID{}) ||
			candidate.RepresentationID == (RepresentationID{}) || candidate.ContentSHA256 == (SHA256{}) ||
			candidate.Range.Start >= candidate.Range.End || candidate.Range.End > maxJSONSafeInteger ||
			len(candidate.SourceIDs) == 0 || !sortedSourceIDs(candidate.SourceIDs) {
			return errors.New("invalid or repeated trail candidate")
		}
		seen[candidate.SegmentID] = len(candidate.Rejected) == 0
		for _, source := range candidate.SourceIDs {
			if !allowed[source] {
				return errors.New("candidate ancestry must be allowed by the trail")
			}
		}
		if len(candidate.Rejected) > 1 || (len(candidate.Rejected) == 1 &&
			!slices.Contains([]string{"content_mismatch", "source_withdrawn", "source_state_missing"}, candidate.Rejected[0])) {
			return errors.New("invalid candidate rejection")
		}
		if len(candidate.Scores) == 0 {
			return errors.New("candidate has no scores")
		}
		for j, score := range candidate.Scores {
			if score.Branch < 0 || score.Branch >= len(branches) || score.Level != "block" ||
				(j > 0 && score.Branch <= candidate.Scores[j-1].Branch) {
				return errors.New("invalid or repeated candidate score")
			}
			if shared {
				if score.BM25 == nil {
					return errors.New("shared BM25 score has no match evidence")
				}
				expected, err := branches[score.Branch].SharedBM25.Score(idfs[score.Branch], *score.BM25)
				bits, parseErr := strconv.ParseUint(score.ValueBits, 16, 64)
				recorded := math.Float64frombits(bits)
				if err != nil || parseErr != nil || math.IsNaN(recorded) || math.IsInf(recorded, 0) ||
					math.Abs(expected-recorded) > 1e-9*max(1, math.Abs(recorded)) {
					return errors.New("candidate score disagrees with shared BM25 evidence")
				}
			} else if score.BM25 != nil {
				return errors.New("legacy score cannot carry shared BM25 evidence")
			}
			proposed[score.Branch]--
		}
		check[i].Scores = slices.Clone(candidate.Scores)
	}
	if proposed != ([4]int{}) {
		return errors.New("candidate scores disagree with proposal counts")
	}
	// Supersession effects must name eligible unrejected candidates in
	// strictly sorted unique order, with sorted unique nonzero replacements
	// that never include the note they replace.
	for i, effect := range trail.Supersession {
		if effect.SegmentID == (SegmentID{}) || effect.NoteID == (SHA256{}) || !seen[effect.SegmentID] ||
			(i > 0 && bytes.Compare(trail.Supersession[i-1].SegmentID[:], effect.SegmentID[:]) >= 0) {
			return errors.New("invalid or repeated supersession effect")
		}
		if len(effect.SupersededBy) == 0 {
			return errors.New("supersession effect has no replacements")
		}
		for j, replacement := range effect.SupersededBy {
			if replacement == (SHA256{}) || replacement == effect.NoteID ||
				(j > 0 && bytes.Compare(effect.SupersededBy[j-1][:], replacement[:]) >= 0) {
				return errors.New("invalid supersession effect replacement")
			}
		}
	}
	if err := RankCandidates(check, len(branches)); err != nil {
		return err
	}
	// An include request records the effects but never omits for them.
	var packSupersession []SupersessionEffect
	if !trail.Request.IncludeSuperseded {
		packSupersession = trail.Supersession
	}
	packing := ApplyFirstFit(check, trail.Request.Budget.Limit, packSupersession)
	if packing != *trail.Packing {
		return errors.New("packing total or policy disagrees with replay")
	}
	for i, candidate := range trail.Candidates {
		want := check[i]
		if candidate.SegmentID != want.SegmentID || candidate.Rank != want.Rank ||
			!slices.Equal(candidate.Scores, want.Scores) {
			return errors.New("candidate order or ranks disagree with replay")
		}
		if candidate.Packed != want.Packed || candidate.Omitted != want.Omitted ||
			candidate.DuplicateOf != want.DuplicateOf || candidate.Cost != want.Cost ||
			len(candidate.Extracts) != 0 {
			return errors.New("candidate packing disagrees with replay")
		}
	}
	return nil
}

// RankCandidates ranks accepted scores by numeric value per branch, then
// sorts the candidate list by single-branch rank or exact reciprocal rank.
func RankCandidates(candidates []Candidate, branchCount int) error {
	type scored struct {
		index, entry int
		value        float64
	}
	var ranked [4][]scored
	if branchCount < 1 || branchCount > len(ranked) {
		return errors.New("invalid branch count")
	}
	for i := range candidates {
		c := &candidates[i]
		if len(c.Scores) == 0 {
			return errors.New("candidate has no scores")
		}
		c.Rank = 0
		for j := range c.Scores {
			score := &c.Scores[j]
			score.Rank = 0
			bits, err := strconv.ParseUint(score.ValueBits, 16, 64)
			value := math.Float64frombits(bits)
			if err != nil || len(score.ValueBits) != 16 || strings.ToLower(score.ValueBits) != score.ValueBits ||
				math.IsNaN(value) || math.IsInf(value, 0) || score.Branch < 0 || score.Branch >= branchCount {
				return errors.New("invalid candidate score bits or branch")
			}
			if len(c.Rejected) == 0 {
				ranked[score.Branch] = append(ranked[score.Branch], scored{i, j, value})
			}
		}
	}
	for _, branch := range ranked[:branchCount] {
		slices.SortFunc(branch, func(a, b scored) int {
			aid, bid := candidates[a.index].SegmentID, candidates[b.index].SegmentID
			return cmp.Or(cmp.Compare(a.value, b.value), bytes.Compare(aid[:], bid[:]))
		})
		for rank, score := range branch {
			candidates[score.index].Scores[score.entry].Rank = rank + 1
		}
	}

	fused := make(map[SegmentID]*big.Rat)
	if branchCount > 1 {
		for _, c := range candidates {
			if len(c.Rejected) == 0 {
				sum := new(big.Rat)
				for _, score := range c.Scores {
					sum.Add(sum, big.NewRat(1, 60+int64(score.Rank)))
				}
				fused[c.SegmentID] = sum
			}
		}
	}
	slices.SortFunc(candidates, func(a, b Candidate) int {
		if n := cmp.Compare(len(a.Rejected), len(b.Rejected)); n != 0 {
			return n
		}
		if len(a.Rejected) == 0 {
			if branchCount == 1 {
				return cmp.Compare(a.Scores[0].Rank, b.Scores[0].Rank)
			}
			if n := fused[b.SegmentID].Cmp(fused[a.SegmentID]); n != 0 {
				return n
			}
		}
		return bytes.Compare(a.SegmentID[:], b.SegmentID[:])
	})
	for i := range candidates {
		if len(candidates[i].Rejected) == 0 {
			candidates[i].Rank = i + 1
		}
	}
	return nil
}

// ApplyFirstFit packs whole segments, skipping over budget omissions and
// suppressing duplicates of segments already packed. Every segment a
// supersession effect names is omitted; callers pass nil when the request
// asked for superseded passages.
func ApplyFirstFit(candidates []Candidate, budget uint64, supersession []SupersessionEffect) Packing {
	excluded := make(map[SegmentID]bool, len(supersession))
	for _, effect := range supersession {
		excluded[effect.SegmentID] = true
	}
	packing := Packing{Policy: "first-fit-v1"}
	packed := make(map[SHA256]SegmentID)
	for i := range candidates {
		c := &candidates[i]
		c.Packed, c.Omitted, c.DuplicateOf, c.Cost = "none", "", SegmentID{}, 0
		if len(c.Rejected) != 0 {
			continue
		}
		if excluded[c.SegmentID] {
			c.Omitted = "superseded"
			continue
		}
		size := c.Range.End - c.Range.Start
		if previous, ok := packed[c.ContentSHA256]; ok {
			c.Omitted, c.DuplicateOf = "duplicate", previous
		} else if size > budget-packing.Used {
			c.Omitted = "budget"
		} else {
			c.Packed, c.Cost = "full", size
			packing.Used += size
			packed[c.ContentSHA256] = c.SegmentID
		}
	}
	return packing
}
