// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"
)

// DetectState is a geofence transition a fence can report. Restrict a fence to
// a subset with Detect; omitting it means Tile38's default set.
type DetectState string

// Geofence transitions.
const (
	Inside  DetectState = "inside"  // object is within the fence
	Outside DetectState = "outside" // object is outside the fence
	Enter   DetectState = "enter"   // object crossed in
	Exit    DetectState = "exit"    // object crossed out
	Cross   DetectState = "cross"   // object passed through
)

// Command is a Tile38 command that can cause a fence event. Restrict a fence to
// a subset with Commands.
type Command string

// Commands that produce fence events.
const (
	CommandSet    Command = "set"
	CommandFSet   Command = "fset"
	CommandDel    Command = "del"
	CommandPDel   Command = "pdel"
	CommandDrop   Command = "drop"
	CommandExpire Command = "expire"
)

// GlobalBounds is the whole-world bounding box. Go binds a multi-valued call
// straight onto a matching parameter list, so it can be handed to any Bounds
// method as-is:
//
//	c.SetChan("zone").Within("fleet").Bounds(tile38.GlobalBounds())
func GlobalBounds() (swLat, swLon, neLat, neLon float64) {
	return -90, -180, 90, 180
}

// joinTokens renders a list of string-backed protocol tokens as the
// comma-separated form Tile38 expects.
func joinTokens[T ~string](tokens []T) string {
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = string(t)
	}
	return strings.Join(parts, ",")
}

// field is an unexported key-value pair used by builder types.
type field struct {
	name  string
	value any
}

// Field creates a key/value field for use with the Field and Fields methods.
// The field type stays unexported; Field is the only public entry point.
func Field(name string, value any) field {
	return field{name: name, value: value}
}

// searchOpts holds the search options Tile38 rejects a second occurrence of
// (errDuplicateArgument in internal/server/token.go). Storing them as values and
// rendering them once means calling Limit or Sparse twice overwrites instead of
// emitting a duplicate the server would reject. Repeatable options — WHERE,
// WHEREIN, MATCH — stay in args, where accumulating is the intended behaviour.
type searchOpts struct {
	limit    *int
	cursor   *uint64
	sparse   *int
	order    *string // ASC or DESC; SCAN and SEARCH only
	nofields bool
	clip     bool
}

// fenceOpts drops the options Tile38 rejects on a live fence: "CURSOR is not
// allowed when FENCE is specified" (token.go). The value receiver copies, so the
// builder's own options are untouched.
func (o searchOpts) fenceOpts() searchOpts {
	o.cursor = nil
	return o
}

// tokens renders the option clause. Order within it is free: Tile38's token
// parser loops over these until it hits the output format.
func (o searchOpts) tokens() []any {
	out := make([]any, 0, 8)
	if o.cursor != nil {
		out = append(out, "CURSOR", *o.cursor)
	}
	if o.limit != nil {
		out = append(out, "LIMIT", *o.limit)
	}
	if o.sparse != nil {
		out = append(out, "SPARSE", *o.sparse)
	}
	if o.order != nil {
		out = append(out, *o.order)
	}
	if o.nofields {
		out = append(out, "NOFIELDS")
	}
	if o.clip {
		out = append(out, "CLIP")
	}
	return out
}

// countedTokens renders the length-prefixed option shape Tile38 uses for
// WHEREIN, WHEREEVAL and WHEREEVALSHA alike: <keyword> <subject> <count> value…
func countedTokens(keyword, subject string, values []any) []any {
	out := make([]any, 0, len(values)+3)
	out = append(out, keyword, subject, len(values))
	return append(out, values...)
}

// whereInTokens renders WHEREIN field <count> value…
func whereInTokens(field string, values []any) []any {
	return countedTokens("WHEREIN", field, values)
}

// buildSearch assembles a search command in the order Tile38's parser requires:
// verb and key, then options, then the fence clause, then the output format,
// then the search area. Builders keep those parts separate, so the order the
// caller chains them in cannot affect the command that goes out.
func buildSearch(args []any, opts searchOpts, fence []any, format []string, geom []any) []any {
	tokens := opts.tokens()
	out := make([]any, 0, len(args)+len(tokens)+len(fence)+len(format)+len(geom))
	out = append(out, args...)
	out = append(out, tokens...)
	out = append(out, fence...)
	for _, f := range format {
		out = append(out, f)
	}
	return append(out, geom...)
}

// searchState is everything a search builder holds that does not depend on its
// output type. The builders share it by pointer within one chain so a format
// switch can re-wrap without copying the chain apart, and clone it when the
// output type changes so two typed handles never mutate one another.
type searchState struct {
	c         *Client
	verb      string // NEARBY, WITHIN, INTERSECTS, SCAN, SEARCH — for error text
	args      []any  // verb, key, and repeatable options
	opts      searchOpts
	cursorOut uint64 // cursor from the last executed Do
	detect    []DetectState
	commands  []Command
	nodwell   bool
	distance  bool
	geom      []any // search area, empty for SCAN and SEARCH
	radius    *int  // trailing radius of a POINT area, NEARBY only
}

// clone deep-copies the slices a builder appends to, so a format switch hands
// back an independent chain. The pointers in opts are only ever replaced, never
// written through, so copying the header is enough for them.
func (s *searchState) clone() *searchState {
	c := *s
	c.args = slices.Clone(s.args)
	c.geom = slices.Clone(s.geom)
	c.detect = slices.Clone(s.detect)
	c.commands = slices.Clone(s.commands)
	return &c
}

// geometry returns the search area with its trailing radius. SCAN and SEARCH
// carry no area and the non-NEARBY verbs carry no radius, so this is correct
// for every builder: pointGeometry returns geom untouched in both cases.
func (s *searchState) geometry() []any {
	return pointGeometry(s.geom, s.radius)
}

// execArgs assembles the command. No fence clause: Detect, Commands and
// Distance apply to Fence alone.
func (s *searchState) execArgs(format []string) []any {
	return buildSearch(s.args, s.opts, nil, format, s.geometry())
}

// fenceArgs assembles the live-fence form: the FENCE clause replaces the output
// format, and CURSOR is dropped because Tile38 rejects it alongside FENCE.
func (s *searchState) fenceArgs() []any {
	return buildSearch(s.args, s.opts.fenceOpts(),
		fenceTokens(s.distance, s.detect, s.commands, s.nodwell), nil, s.geometry())
}

// format is an output format's protocol tokens, its keyword for error text, and
// the decoder for the reply those tokens produce.
//
// Carrying the decoder is what lets the builders be parameterised by the element
// type: the decoding no longer has to be found on the result type at run time,
// so nothing needs to hang a method off E and IDS can decode to a plain string.
type format[E any] struct {
	name   string
	tokens []string
	decode func(prefix string, reply any) ([]E, uint64, error)
}

// The formats every search verb accepts. STRINGS emits no token because that
// shape is what SEARCH returns when none is given. COUNT is absent: its reply is
// a bare integer with no element type, so it is a terminal rather than a format.
var (
	formatIDs      = format[string]{"IDS", []string{"IDS"}, parseScanIDs}
	formatPoints   = format[NearbyResult]{"POINTS", []string{"POINTS"}, parsePoints}
	formatDistance = format[NearbyResultWithDistance]{"DISTANCE POINTS", []string{"DISTANCE", "POINTS"}, parsePointsWithDistance}
	formatObjects  = format[SearchObject]{"OBJECTS", []string{"OBJECTS"}, parseObjects}
	formatRects    = format[RectResult]{"BOUNDS", []string{"BOUNDS"}, parseRects}
	formatStrings  = format[StringObject]{"STRINGS", nil, parseStrings}
)

// DISTANCE is an option token, so formatDistance renders it ahead of the output
// format rather than after it.

func formatHashes(precision int) format[HashResult] {
	return format[HashResult]{"HASHES", []string{"HASHES", strconv.Itoa(precision)}, parseHashes}
}

func formatA5Cells(level int) format[A5Result] {
	return format[A5Result]{"A5", []string{"A5", strconv.Itoa(level)}, parseA5Cells}
}

// searchDo runs the assembled command and decodes it with the format's own
// decoder.
//
// There is nothing to discover at run time: a builder holds a format[E] and
// every format carries a decoder, so a builder whose element type does not match
// its format cannot be constructed.
func searchDo[E any](ctx context.Context, s *searchState, f format[E]) ([]E, error) {
	val, err := s.c.do(ctx, s.execArgs(f.tokens)...)
	if err != nil {
		return nil, fmt.Errorf("tile38: %s %s: %w", s.verb, f.name, err)
	}
	res, cursor, err := f.decode(s.verb+" "+f.name, val)
	if err != nil {
		return nil, err
	}
	s.cursorOut = cursor
	return res, nil
}

// searchIter pages a search to completion, yielding one item at a time so the
// caller never sees a page boundary. A non-zero cursor is its loop condition.
//
// An explicit Limit or Cursor is the caller's own bound, so it yields that one
// page and stops.
//
// The cursor is driven on searchState and restored when the range ends, so a
// second Iter on the same builder starts over rather than resuming mid-scan, and
// an early break leaves the builder as it found it. Breaking out simply stops
// asking for pages: each one is an ordinary pooled round trip, with no
// connection held open between them.
func searchIter[E any](ctx context.Context, s *searchState, f format[E]) iter.Seq2[E, error] {
	return func(yield func(E, error) bool) {
		start := s.opts.cursor
		bounded := s.opts.limit != nil || start != nil
		defer func() { s.opts.cursor = start }()

		for {
			items, err := searchDo(ctx, s, f)
			if err != nil {
				var zero E
				yield(zero, err)
				return
			}
			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}
			if bounded || s.cursorOut == 0 {
				return
			}
			next := s.cursorOut
			s.opts.cursor = &next
		}
	}
}

// searchCount runs the COUNT form, which is a terminal rather than an output
// format: the reply is a bare integer, so there is no element type to
// parameterise a builder by, no cursor to record, and no truncation to report —
// the server exempts COUNT from the hundred-result cap.
func searchCount(ctx context.Context, s *searchState) (int, error) {
	val, err := s.c.do(ctx, s.execArgs([]string{"COUNT"})...)
	if err != nil {
		return 0, fmt.Errorf("tile38: %s COUNT: %w", s.verb, err)
	}
	return parseCount(s.verb, val)
}

// pointGeometry appends the trailing metres of Tile38's "POINT lat lon meters",
// and only to a POINT: a ROAM area carries its own radius, so attaching it there
// would malform the command whichever order the two were chained in.
func pointGeometry(geom []any, radius *int) []any {
	if radius == nil || len(geom) == 0 || geom[0] != "POINT" {
		return geom
	}
	return append(append([]any{}, geom...), *radius)
}

// hookHead renders the META and EX clauses, which sit between a hook's name and
// endpoint and its spatial trigger. Tile38 parses both in the same loop, so
// their order relative to each other is free.
func hookHead(head []any, meta [][2]string, ex *int) []any {
	for _, m := range meta {
		head = append(head, "META", m[0], m[1])
	}
	if ex != nil {
		head = append(head, "EX", *ex)
	}
	return head
}

// fenceTokens renders the FENCE clause. DETECT and COMMANDS are single-use in
// Tile38, so they are stored as values and rendered once here. DISTANCE is an
// option rather than part of the clause, so it precedes FENCE — and it is
// rendered here, alongside DETECT, precisely so it can never leak onto a plain
// query, where the trailing distance it adds would shift every item's shape.
func fenceTokens(distance bool, detect []DetectState, commands []Command, nodwell bool) []any {
	out := make([]any, 0, 7)
	if distance {
		out = append(out, "DISTANCE")
	}
	out = append(out, "FENCE")
	if nodwell {
		out = append(out, "NODWELL")
	}
	if len(detect) > 0 {
		out = append(out, "DETECT", joinTokens(detect))
	}
	if len(commands) > 0 {
		out = append(out, "COMMANDS", joinTokens(commands))
	}
	return out
}

// ── Write commands ────────────────────────────────────────────────────────────

// SetCmd builds a Tile38 SET command.
// Ordering contract: chain TTL/NX/XX then Field/Fields then one geometry method (At/Object/Bounds/Hash/String).
type SetCmd struct {
	c    *Client
	args []any
}

// EX sets the expiry in seconds, matching Tile38's EX keyword. Zero means no expiry.
func (cmd *SetCmd) EX(secs int) *SetCmd {
	if secs > 0 {
		cmd.args = append(cmd.args, "EX", secs)
	}
	return cmd
}

// NX causes SET to be a no-op if the object already exists.
func (cmd *SetCmd) NX() *SetCmd {
	cmd.args = append(cmd.args, "NX")
	return cmd
}

// XX causes SET to be a no-op if the object does not exist.
func (cmd *SetCmd) XX() *SetCmd {
	cmd.args = append(cmd.args, "XX")
	return cmd
}

// Field appends a single named field to the SET command.
func (cmd *SetCmd) Field(name string, value any) *SetCmd {
	cmd.args = append(cmd.args, "FIELD", name, value)
	return cmd
}

// Fields appends multiple named fields to the SET command in one call.
func (cmd *SetCmd) Fields(fields ...field) *SetCmd {
	for _, f := range fields {
		cmd.args = append(cmd.args, "FIELD", f.name, f.value)
	}
	return cmd
}

// PointZ stores the object as a POINT carrying a third ordinate, which Tile38
// keeps and hands back through PointZ and NearbyResult.Z. A z of zero is stored
// as a plain two-dimensional point.
func (cmd *SetCmd) PointZ(lat, lon, z float64) *SetCmd {
	cmd.args = append(cmd.args, "POINT", lat, lon, z)
	return cmd
}

// Point stores the object as a POINT at the given coordinates.
func (cmd *SetCmd) Point(lat, lon float64) *SetCmd {
	cmd.args = append(cmd.args, "POINT", lat, lon)
	return cmd
}

// Object stores a GeoJSON string as the object's geometry.
func (cmd *SetCmd) Object(geojson string) *SetCmd {
	cmd.args = append(cmd.args, "OBJECT", geojson)
	return cmd
}

// Bounds stores the object as a bounding box.
func (cmd *SetCmd) Bounds(swLat, swLon, neLat, neLon float64) *SetCmd {
	cmd.args = append(cmd.args, "BOUNDS", swLat, swLon, neLat, neLon)
	return cmd
}

// Hash stores the object from a geohash string.
func (cmd *SetCmd) Hash(geohash string) *SetCmd {
	cmd.args = append(cmd.args, "HASH", geohash)
	return cmd
}

// String stores a plain string value (non-spatial).
func (cmd *SetCmd) String(value string) *SetCmd {
	cmd.args = append(cmd.args, "STRING", value)
	return cmd
}

// Do executes the SET command.
func (cmd *SetCmd) Do(ctx context.Context) error {
	_, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return fmt.Errorf("tile38: SET: %w", err)
	}
	return nil
}

// DelCmd builds a Tile38 DEL command.
type DelCmd struct {
	c    *Client
	args []any
}

// Do executes: DEL collection id
func (cmd *DelCmd) Do(ctx context.Context) error {
	_, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return fmt.Errorf("tile38: DEL: %w", err)
	}
	return nil
}

// FSetCmd builds a Tile38 FSET command.
type FSetCmd struct {
	c    *Client
	args []any
}

// Field appends a single named field to update.
func (cmd *FSetCmd) Field(name string, value any) *FSetCmd {
	cmd.args = append(cmd.args, name, value)
	return cmd
}

// Fields appends multiple named fields to update in one call.
func (cmd *FSetCmd) Fields(fields ...field) *FSetCmd {
	for _, f := range fields {
		cmd.args = append(cmd.args, f.name, f.value)
	}
	return cmd
}

// Do executes: FSET collection id [name val ...]
func (cmd *FSetCmd) Do(ctx context.Context) error {
	_, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return fmt.Errorf("tile38: FSET: %w", err)
	}
	return nil
}

// FGetCmd builds a Tile38 FGET command.
type FGetCmd struct {
	c    *Client
	args []any
}

// Do executes: FGET collection id field
//
// It returns the raw string value of the field. A missing field and an empty one
// are indistinguishable: Tile38 replies with the zero value of the field either
// way — "" for a string field, "0" for a numeric one — rather than a null. A
// missing collection or object does produce an error, so only the field name is
// ambiguous.
func (cmd *FGetCmd) Do(ctx context.Context) (string, error) {
	val, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return "", fmt.Errorf("tile38: FGET: %w", err)
	}
	return toString("FGET", val)
}

// ── Read commands ─────────────────────────────────────────────────────────────

// GetCmd builds a Tile38 GET command.
type GetCmd struct {
	c          *Client
	args       []any
	withFields bool
	fields     Fields // recorded by the last terminal when WithFields is set
}

// WithFields asks for the object's fields alongside its geometry, matching
// Tile38's WITHFIELDS keyword. It applies to every output format; read the
// fields with Fields once the terminal has returned:
//
//	g := c.Get("fleet", "truck1").WithFields()
//	lat, lon, err := g.Point(ctx)
//	speed := g.Fields()["speed"]
//
// One GET then answers what would otherwise take an FGet per field.
func (cmd *GetCmd) WithFields() *GetCmd {
	cmd.withFields = true
	return cmd
}

// Fields returns the fields read by the most recent terminal. It is nil until a
// terminal has run, when WithFields was not chained, or when the object has no
// non-zero fields — Tile38 omits the fields element entirely in that case.
func (cmd *GetCmd) Fields() Fields { return cmd.fields }

// exec runs the GET and unwraps the envelope WITHFIELDS puts around the reply:
// [value] for an object with no non-zero fields and [value, [name, val, …]]
// otherwise. Doing it here is what lets every output format carry fields.
func (cmd *GetCmd) exec(ctx context.Context, format ...any) (any, error) {
	args := make([]any, 0, len(cmd.args)+len(format)+1)
	args = append(args, cmd.args...)
	if cmd.withFields {
		args = append(args, "WITHFIELDS")
	}
	val, err := cmd.c.do(ctx, append(args, format...)...)
	if err != nil {
		return nil, err
	}
	// Over RESP a miss is a null reply: the "id not found" text upstream
	// carries is the JSON output mode's (internal/server/crud.go), so it never
	// reaches this client. Reporting it here rather than in each terminal keeps
	// every output format answering a miss the same way.
	if val == nil {
		return nil, ErrIDNotFound
	}
	if !cmd.withFields {
		return val, nil
	}
	outer, ok := val.([]any)
	if !ok || len(outer) == 0 {
		return nil, fmt.Errorf("tile38: GET WITHFIELDS: %w: response shape %T", ErrUnexpectedReply, val)
	}
	cmd.fields = nil
	if len(outer) > 1 {
		cmd.fields = parseFields(outer[1])
	}
	return outer[0], nil
}

// Point executes: GET collection id POINT — returns the lat/lon of the object.
// Use PointZ for an object stored with a third ordinate.
func (cmd *GetCmd) Point(ctx context.Context) (lat, lon float64, err error) {
	lat, lon, _, err = cmd.PointZ(ctx)
	return lat, lon, err
}

// PointZ executes: GET collection id POINT — returns the lat/lon of the object
// along with its third ordinate, which Tile38 appends only when it is non-zero.
func (cmd *GetCmd) PointZ(ctx context.Context) (lat, lon, z float64, err error) {
	val, err := cmd.exec(ctx, "POINT")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("tile38: GET POINT: %w", err)
	}
	return parseCoords("GET POINT", val)
}

// Object executes: GET collection id — returns the raw GeoJSON string.
func (cmd *GetCmd) Object(ctx context.Context) (string, error) {
	val, err := cmd.exec(ctx)
	if err != nil {
		return "", fmt.Errorf("tile38: GET: %w", err)
	}
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("tile38: GET: %w: response type %T", ErrUnexpectedReply, val)
	}
	return s, nil
}

// Bounds executes: GET collection id BOUNDS — returns the bounding box of the object.
func (cmd *GetCmd) Bounds(ctx context.Context) (BoundsResult, error) {
	val, err := cmd.exec(ctx, "BOUNDS")
	if err != nil {
		return BoundsResult{}, fmt.Errorf("tile38: GET BOUNDS: %w", err)
	}
	return parseBoundsResult("GET", val, false)
}

// Hash executes: GET collection id HASH precision — returns the geohash at the given precision.
func (cmd *GetCmd) Hash(ctx context.Context, precision int) (string, error) {
	val, err := cmd.exec(ctx, "HASH", precision)
	if err != nil {
		return "", fmt.Errorf("tile38: GET HASH: %w", err)
	}
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("tile38: GET HASH: %w: response type %T", ErrUnexpectedReply, val)
	}
	return s, nil
}

// A5 executes: GET collection id A5 level — returns the id of the A5 cell the
// object's centre falls in, which is what WithinCmd.A5 and IntersectsCmd.A5
// take as their area. Requires a server built from upstream master: A5 is
// merged upstream but has shipped in no release tag as of 1.38.0.
func (cmd *GetCmd) A5(ctx context.Context, level int) (string, error) {
	val, err := cmd.exec(ctx, "A5", level)
	if err != nil {
		return "", fmt.Errorf("tile38: GET A5: %w", err)
	}
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("tile38: GET A5: %w: response type %T", ErrUnexpectedReply, val)
	}
	return s, nil
}

// ── Spatial search ────────────────────────────────────────────────────────────

// NearbyCmd builds a Tile38 NEARBY command. Methods may be chained in any
// order; the parts are assembled into protocol order when the command runs.
//
// The type parameter is the element type: Do returns []E. A fresh command is
// NearbyCmd[string] and emits IDS; an output-format method — Points, Objects,
// Rects, Hashes, A5Cells, PointsWithDistance — hands back a command at the
// matching element type. Options chain the same either side of that switch.
//
// Count and Fence are terminals rather than formats: COUNT replies with a bare
// integer and Fence returns a Stream, so neither has an element type.
type NearbyCmd[E any] struct {
	*searchState
	out format[E]
}

// Limit caps the number of results. Zero means no limit.
func (cmd *NearbyCmd[E]) Limit(n int) *NearbyCmd[E] {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a search from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported. Tile38 rejects CURSOR on a
// fence, so Fence ignores it.
func (cmd *NearbyCmd[E]) Cursor(n uint64) *NearbyCmd[E] {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed Do. It is non-zero
// only when Tile38 stopped at the limit with more objects matching.
func (cmd *NearbyCmd[E]) NextCursor() uint64 { return cmd.cursorOut }

// Where sets an optional Tile38 field expression filter.
func (cmd *NearbyCmd[E]) Where(expr string) *NearbyCmd[E] {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// Match filters results by ID pattern (glob-style, e.g. "truck:*").
func (cmd *NearbyCmd[E]) Match(pattern string) *NearbyCmd[E] {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *NearbyCmd[E]) WhereIn(field string, values ...any) *NearbyCmd[E] {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
func (cmd *NearbyCmd[E]) NoFields() *NearbyCmd[E] {
	cmd.opts.nofields = true
	return cmd
}

// Sparse spreads results evenly over the search area at the given depth (1-8),
// matching Tile38's SPARSE keyword. Tile38 rejects SPARSE combined with Limit.
func (cmd *NearbyCmd[E]) Sparse(depth int) *NearbyCmd[E] {
	cmd.opts.sparse = &depth
	return cmd
}

// Detect restricts a live fence to the given transitions. Only meaningful with Fence.
func (cmd *NearbyCmd[E]) Detect(states ...DetectState) *NearbyCmd[E] {
	cmd.detect = states
	return cmd
}

// Commands restricts a live fence to events caused by the given commands.
// Only meaningful with Fence.
func (cmd *NearbyCmd[E]) Commands(commands ...Command) *NearbyCmd[E] {
	cmd.commands = commands
	return cmd
}

// Distance adds each object's distance from the fence centre to every event the
// fence produces, matching Tile38's DISTANCE keyword. It arrives on FenceEvent
// as Distance, and applies to the live fence only — a plain query reads the same
// value through PointsWithDistance.
func (cmd *NearbyCmd[E]) Distance() *NearbyCmd[E] {
	cmd.distance = true
	return cmd
}

// WhereEval keeps results for which the given Lua script returns true, matching
// Tile38's WHEREEVAL keyword. The script sees the object's fields as FIELDS and
// the extra arguments as ARGV. It accumulates: each call adds another filter.
func (cmd *NearbyCmd[E]) WhereEval(script string, args ...any) *NearbyCmd[E] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVAL", script, args)...)
	return cmd
}

// WhereEvalSha is WhereEval against a script already loaded on the server,
// matching Tile38's WHEREEVALSHA keyword.
func (cmd *NearbyCmd[E]) WhereEvalSha(sha string, args ...any) *NearbyCmd[E] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVALSHA", sha, args)...)
	return cmd
}

// Point sets the centre coordinates.
func (cmd *NearbyCmd[E]) Point(lat, lon float64) *NearbyCmd[E] {
	cmd.geom = []any{"POINT", lat, lon}
	return cmd
}

// Radius sets the search radius in metres. It applies to a Point area; a Roam
// area carries its own radius.
func (cmd *NearbyCmd[E]) Radius(metres int) *NearbyCmd[E] {
	cmd.radius = &metres
	return cmd
}

// Roam turns the fence into a roaming one: it fires as objects in this
// collection move within radiusM metres of an object in collection. Tile38
// accepts ROAM only on a live NEARBY fence, so this needs Fence — Do will be
// rejected by the server.
func (cmd *NearbyCmd[E]) Roam(collection string, radiusM int) *NearbyCmd[E] {
	cmd.geom = []any{"ROAM", collection, "*", radiusM}
	return cmd
}

// NoDwell stops a roaming fence from re-reporting objects that stay within
// range between updates, matching Tile38's NODWELL keyword. It only affects
// Roam fences.
func (cmd *NearbyCmd[E]) NoDwell() *NearbyCmd[E] {
	cmd.nodwell = true
	return cmd
}

// IDs selects the IDS output format: NEARBY collection [opts] IDS POINT lat lon radius.
// It is what a fresh command already emits, and is here to switch back.
func (cmd *NearbyCmd[E]) IDs() *NearbyCmd[string] {
	return &NearbyCmd[string]{cmd.clone(), formatIDs}
}

// Points selects the POINTS output format: NEARBY collection [opts] POINTS POINT lat lon radius.
func (cmd *NearbyCmd[E]) Points() *NearbyCmd[NearbyResult] {
	return &NearbyCmd[NearbyResult]{cmd.clone(), formatPoints}
}

// PointsWithDistance selects POINTS with each object's distance from the search
// centre: NEARBY collection [opts] DISTANCE POINTS POINT lat lon radius.
// DISTANCE is an option token, so it has to precede the output format.
func (cmd *NearbyCmd[E]) PointsWithDistance() *NearbyCmd[NearbyResultWithDistance] {
	return &NearbyCmd[NearbyResultWithDistance]{cmd.clone(), formatDistance}
}

// Objects selects the OBJECTS output format: NEARBY collection [opts] OBJECTS POINT lat lon radius.
func (cmd *NearbyCmd[E]) Objects() *NearbyCmd[SearchObject] {
	return &NearbyCmd[SearchObject]{cmd.clone(), formatObjects}
}

// Rects selects the BOUNDS output format: NEARBY collection [opts] BOUNDS POINT lat lon radius.
// Each result is the bounding box of a matching object, lat first.
func (cmd *NearbyCmd[E]) Rects() *NearbyCmd[RectResult] {
	return &NearbyCmd[RectResult]{cmd.clone(), formatRects}
}

// Hashes selects the HASHES output format: NEARBY collection [opts] HASHES precision POINT lat lon radius.
// Each result is the geohash of a matching object's centre.
func (cmd *NearbyCmd[E]) Hashes(precision int) *NearbyCmd[HashResult] {
	return &NearbyCmd[HashResult]{cmd.clone(), formatHashes(precision)}
}

// A5Cells selects the A5 output format: NEARBY collection [opts] A5 level POINT lat lon radius.
// Each result is the A5 cell a matching object's centre falls in. Named for the
// output rather than the keyword because A5 is already the search-area method on
// the builders that take one. Requires a server built from upstream master.
func (cmd *NearbyCmd[E]) A5Cells(level int) *NearbyCmd[A5Result] {
	return &NearbyCmd[A5Result]{cmd.clone(), formatA5Cells(level)}
}

// Do executes the command in whichever output format was selected, defaulting
// to IDS. It is one round trip and returns one page.
//
// Tile38 caps every output except COUNT at 100 results when the command carries
// no LIMIT (limitItems, internal/server/scanner.go), so a query that is complete
// against a small collection quietly returns a prefix once that collection
// grows. Truncation is not an error: NextCursor is non-zero when the server
// stopped early, and Iter pages past the cap instead.
func (cmd *NearbyCmd[E]) Do(ctx context.Context) ([]E, error) {
	return searchDo(ctx, cmd.searchState, cmd.out)
}

// Iter pages the search to completion, yielding one result at a time in whichever
// output format was selected, following the cursor itself so the hundred-result
// cap never truncates what the caller sees.
//
//	for obj, err := range cmd.Objects().Iter(ctx) {
//		if err != nil {
//			return err
//		}
//		use(obj)
//	}
//
// An explicit Limit or Cursor is the caller's own bound, so Iter yields that one
// page rather than paging past it. Breaking out of the range just stops asking
// for pages; nothing is left open.
func (cmd *NearbyCmd[E]) Iter(ctx context.Context) iter.Seq2[E, error] {
	return searchIter(ctx, cmd.searchState, cmd.out)
}

// Count runs the COUNT form: NEARBY collection [opts] COUNT POINT lat lon radius.
// It returns the number of matching objects.
//
// COUNT is a terminal rather than an output format: its reply is a bare
// integer, so there is no element type for a builder to carry, and the
// hundred-result cap does not apply: the server exempts COUNT from it.
func (cmd *NearbyCmd[E]) Count(ctx context.Context) (int, error) {
	return searchCount(ctx, cmd.searchState)
}

// Fence opens a live geofence: NEARBY collection [opts] FENCE [DETECT …] POINT lat lon radius.
// The returned Stream holds a dedicated connection and delivers events until it
// is closed or ctx is cancelled.
func (cmd *NearbyCmd[E]) Fence(ctx context.Context) (*Stream, error) {
	return cmd.c.fenceStream(ctx, cmd.fenceArgs())
}

// ScanCmd builds a Tile38 SCAN command.
//
// The type parameter is the element type Do returns a slice of; see NearbyCmd.
// SCAN takes no search area and no fence.
type ScanCmd[E any] struct {
	*searchState
	out format[E]
}

// Limit caps the number of results. Zero means no limit.
func (cmd *ScanCmd[E]) Limit(n int) *ScanCmd[E] {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a scan from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported.
func (cmd *ScanCmd[E]) Cursor(n uint64) *ScanCmd[E] {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed Do. It is non-zero
// only when Tile38 stopped at the limit with more objects matching.
func (cmd *ScanCmd[E]) NextCursor() uint64 { return cmd.cursorOut }

// Match filters results by ID pattern (glob-style, e.g. "truck:*").
func (cmd *ScanCmd[E]) Match(pattern string) *ScanCmd[E] {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// Where sets an optional Tile38 field expression filter.
func (cmd *ScanCmd[E]) Where(expr string) *ScanCmd[E] {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *ScanCmd[E]) WhereIn(field string, values ...any) *ScanCmd[E] {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
func (cmd *ScanCmd[E]) NoFields() *ScanCmd[E] {
	cmd.opts.nofields = true
	return cmd
}

// Asc returns results in ascending ID order, matching Tile38's ASC keyword.
// Only SCAN and SEARCH take an order — the spatial verbs answer
// "ASC is not allowed for NEARBY". Asc and Desc overwrite each other: Tile38
// rejects a command carrying both.
func (cmd *ScanCmd[E]) Asc() *ScanCmd[E] {
	order := "ASC"
	cmd.opts.order = &order
	return cmd
}

// Desc returns results in descending ID order, matching Tile38's DESC keyword.
// See Asc for why it is single-use.
func (cmd *ScanCmd[E]) Desc() *ScanCmd[E] {
	order := "DESC"
	cmd.opts.order = &order
	return cmd
}

// WhereEval keeps results for which the given Lua script returns true, matching
// Tile38's WHEREEVAL keyword. The script sees the object's fields as FIELDS and
// the extra arguments as ARGV. It accumulates: each call adds another filter.
func (cmd *ScanCmd[E]) WhereEval(script string, args ...any) *ScanCmd[E] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVAL", script, args)...)
	return cmd
}

// WhereEvalSha is WhereEval against a script already loaded on the server,
// matching Tile38's WHEREEVALSHA keyword.
func (cmd *ScanCmd[E]) WhereEvalSha(sha string, args ...any) *ScanCmd[E] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVALSHA", sha, args)...)
	return cmd
}

// IDs selects the IDS output format: SCAN collection [opts] IDS.
// It is what a fresh command already emits, and is here to switch back.
func (cmd *ScanCmd[E]) IDs() *ScanCmd[string] {
	return &ScanCmd[string]{cmd.clone(), formatIDs}
}

// Points selects the POINTS output format: SCAN collection [opts] POINTS.
func (cmd *ScanCmd[E]) Points() *ScanCmd[NearbyResult] {
	return &ScanCmd[NearbyResult]{cmd.clone(), formatPoints}
}

// Objects selects the OBJECTS output format: SCAN collection [opts] OBJECTS.
func (cmd *ScanCmd[E]) Objects() *ScanCmd[SearchObject] {
	return &ScanCmd[SearchObject]{cmd.clone(), formatObjects}
}

// Rects selects the BOUNDS output format: SCAN collection [opts] BOUNDS.
// Each result is the bounding box of a matching object, lat first.
func (cmd *ScanCmd[E]) Rects() *ScanCmd[RectResult] {
	return &ScanCmd[RectResult]{cmd.clone(), formatRects}
}

// Hashes selects the HASHES output format: SCAN collection [opts] HASHES precision.
// Each result is the geohash of a matching object's centre.
func (cmd *ScanCmd[E]) Hashes(precision int) *ScanCmd[HashResult] {
	return &ScanCmd[HashResult]{cmd.clone(), formatHashes(precision)}
}

// A5Cells selects the A5 output format: SCAN collection [opts] A5 level.
// Requires a server built from upstream master.
func (cmd *ScanCmd[E]) A5Cells(level int) *ScanCmd[A5Result] {
	return &ScanCmd[A5Result]{cmd.clone(), formatA5Cells(level)}
}

// Do executes the command in whichever output format was selected, defaulting
// to IDS. It is one round trip and returns one page.
//
// Tile38 caps every output except COUNT at 100 results when the command carries
// no LIMIT (limitItems, internal/server/scanner.go), so a query that is complete
// against a small collection quietly returns a prefix once that collection
// grows. Truncation is not an error: NextCursor is non-zero when the server
// stopped early, and Iter pages past the cap instead.
func (cmd *ScanCmd[E]) Do(ctx context.Context) ([]E, error) {
	return searchDo(ctx, cmd.searchState, cmd.out)
}

// Iter pages the scan to completion, yielding one result at a time in whichever
// output format was selected, following the cursor itself so the hundred-result
// cap never truncates what the caller sees.
//
//	for obj, err := range cmd.Objects().Iter(ctx) {
//		if err != nil {
//			return err
//		}
//		use(obj)
//	}
//
// An explicit Limit or Cursor is the caller's own bound, so Iter yields that one
// page rather than paging past it. Breaking out of the range just stops asking
// for pages; nothing is left open.
func (cmd *ScanCmd[E]) Iter(ctx context.Context) iter.Seq2[E, error] {
	return searchIter(ctx, cmd.searchState, cmd.out)
}

// Count runs the COUNT form: SCAN collection [opts] COUNT.
// It returns the number of matching objects.
//
// COUNT is a terminal rather than an output format: its reply is a bare
// integer, so there is no element type for a builder to carry, and the
// hundred-result cap does not apply: the server exempts COUNT from it.
func (cmd *ScanCmd[E]) Count(ctx context.Context) (int, error) {
	return searchCount(ctx, cmd.searchState)
}

// SearchCmd builds a Tile38 SEARCH command, which matches on the string values
// "SET … STRING" stores rather than on geometry. It takes no area and no fence.
//
// The type parameter is the element type Do returns a slice of; see NearbyCmd.
// A fresh command emits IDS; Strings selects SEARCH's own default output, which
// carries each object's string value.
type SearchCmd[E any] struct {
	*searchState
	out format[E]
}

// Limit caps the number of results. Zero means no limit.
func (cmd *SearchCmd[E]) Limit(n int) *SearchCmd[E] {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a search from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported.
func (cmd *SearchCmd[E]) Cursor(n uint64) *SearchCmd[E] {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed Do.
func (cmd *SearchCmd[E]) NextCursor() uint64 { return cmd.cursorOut }

// Match filters results by the object's string value (glob-style).
func (cmd *SearchCmd[E]) Match(pattern string) *SearchCmd[E] {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// Asc returns results in ascending order, matching Tile38's ASC keyword.
// Asc and Desc overwrite each other: Tile38 rejects a command carrying both.
func (cmd *SearchCmd[E]) Asc() *SearchCmd[E] {
	order := "ASC"
	cmd.opts.order = &order
	return cmd
}

// Desc returns results in descending order, matching Tile38's DESC keyword.
// See Asc for why it is single-use.
func (cmd *SearchCmd[E]) Desc() *SearchCmd[E] {
	order := "DESC"
	cmd.opts.order = &order
	return cmd
}

// Where sets an optional Tile38 field expression filter.
func (cmd *SearchCmd[E]) Where(expr string) *SearchCmd[E] {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *SearchCmd[E]) WhereIn(field string, values ...any) *SearchCmd[E] {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// WhereEval keeps results for which the given Lua script returns true, matching
// Tile38's WHEREEVAL keyword. It accumulates: each call adds another filter.
func (cmd *SearchCmd[E]) WhereEval(script string, args ...any) *SearchCmd[E] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVAL", script, args)...)
	return cmd
}

// WhereEvalSha is WhereEval against a script already loaded on the server,
// matching Tile38's WHEREEVALSHA keyword.
func (cmd *SearchCmd[E]) WhereEvalSha(sha string, args ...any) *SearchCmd[E] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVALSHA", sha, args)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
func (cmd *SearchCmd[E]) NoFields() *SearchCmd[E] {
	cmd.opts.nofields = true
	return cmd
}

// IDs selects the IDS output format: SEARCH collection [opts] IDS.
// It is what a fresh command already emits, and is here to switch back.
func (cmd *SearchCmd[E]) IDs() *SearchCmd[string] {
	return &SearchCmd[string]{cmd.clone(), formatIDs}
}

// Strings selects SEARCH's own default output, where element 1 of each item is
// the object's string value rather than its geometry. It emits no format token,
// because that shape is what SEARCH returns when none is given.
func (cmd *SearchCmd[E]) Strings() *SearchCmd[StringObject] {
	return &SearchCmd[StringObject]{cmd.clone(), formatStrings}
}

// Do executes the command in whichever output format was selected, defaulting
// to IDS. It is one round trip and returns one page.
//
// Tile38 caps every output except COUNT at 100 results when the command carries
// no LIMIT (limitItems, internal/server/scanner.go), so a query that is complete
// against a small collection quietly returns a prefix once that collection
// grows. Truncation is not an error: NextCursor is non-zero when the server
// stopped early, and Iter pages past the cap instead.
func (cmd *SearchCmd[E]) Do(ctx context.Context) ([]E, error) {
	return searchDo(ctx, cmd.searchState, cmd.out)
}

// Iter pages the search to completion, yielding one result at a time in whichever
// output format was selected, following the cursor itself so the hundred-result
// cap never truncates what the caller sees.
//
//	for obj, err := range cmd.Objects().Iter(ctx) {
//		if err != nil {
//			return err
//		}
//		use(obj)
//	}
//
// An explicit Limit or Cursor is the caller's own bound, so Iter yields that one
// page rather than paging past it. Breaking out of the range just stops asking
// for pages; nothing is left open.
func (cmd *SearchCmd[E]) Iter(ctx context.Context) iter.Seq2[E, error] {
	return searchIter(ctx, cmd.searchState, cmd.out)
}

// Count runs the COUNT form: SEARCH collection [opts] COUNT.
// It returns the number of matching objects.
//
// COUNT is a terminal rather than an output format: its reply is a bare
// integer, so there is no element type for a builder to carry, and the
// hundred-result cap does not apply: the server exempts COUNT from it.
func (cmd *SearchCmd[E]) Count(ctx context.Context) (int, error) {
	return searchCount(ctx, cmd.searchState)
}

// FExistsCmd builds a Tile38 FEXISTS command.
type FExistsCmd struct {
	c    *Client
	args []any
}

// Do executes: FEXISTS collection id field — reports whether the field is set on
// the object. Unlike FGet, this distinguishes a missing field from one holding
// the zero value.
func (cmd *FExistsCmd) Do(ctx context.Context) (bool, error) {
	val, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return false, fmt.Errorf("tile38: FEXISTS: %w", err)
	}
	n, err := toInt64("FEXISTS", val)
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// WithinCmd builds a Tile38 WITHIN query. Methods may be chained in any order;
// the parts are assembled into protocol order when the command runs.
type WithinCmd[E any] struct {
	*searchState
	out format[E]
}

// Limit caps the number of results. Zero means no limit.
func (cmd *WithinCmd[E]) Limit(n int) *WithinCmd[E] {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a search from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported. Tile38 rejects CURSOR on a
// fence, so Fence ignores it.
func (cmd *WithinCmd[E]) Cursor(n uint64) *WithinCmd[E] {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed terminal. It is
// non-zero only when Tile38 stopped at the limit with more objects matching.
func (cmd *WithinCmd[E]) NextCursor() uint64 { return cmd.cursorOut }

// Where sets an optional Tile38 field expression filter.
func (cmd *WithinCmd[E]) Where(expr string) *WithinCmd[E] {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// Match filters results by ID pattern (glob-style, e.g. "truck:*").
func (cmd *WithinCmd[E]) Match(pattern string) *WithinCmd[E] {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *WithinCmd[E]) WhereIn(field string, values ...any) *WithinCmd[E] {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
func (cmd *WithinCmd[E]) NoFields() *WithinCmd[E] {
	cmd.opts.nofields = true
	return cmd
}

// Clip trims returned objects to the search area rather than returning them
// whole, matching Tile38's CLIP keyword.
func (cmd *WithinCmd[E]) Clip() *WithinCmd[E] {
	cmd.opts.clip = true
	return cmd
}

// Sparse spreads results evenly over the search area at the given depth (1-8),
// matching Tile38's SPARSE keyword. Tile38 rejects SPARSE combined with Limit.
func (cmd *WithinCmd[E]) Sparse(depth int) *WithinCmd[E] {
	cmd.opts.sparse = &depth
	return cmd
}

// Detect restricts a live fence to the given transitions. Only meaningful with Fence.
func (cmd *WithinCmd[E]) Detect(states ...DetectState) *WithinCmd[E] {
	cmd.detect = states
	return cmd
}

// Commands restricts a live fence to events caused by the given commands.
// Only meaningful with Fence.
func (cmd *WithinCmd[E]) Commands(commands ...Command) *WithinCmd[E] {
	cmd.commands = commands
	return cmd
}

// Distance adds each object's distance from the fence centre to every event the
// fence produces, matching Tile38's DISTANCE keyword. It arrives on FenceEvent
// as Distance, and applies to the live fence only — a plain query reads the same
// value through PointsWithDistance.
func (cmd *WithinCmd[E]) Distance() *WithinCmd[E] {
	cmd.distance = true
	return cmd
}

// WhereEval keeps results for which the given Lua script returns true, matching
// Tile38's WHEREEVAL keyword. The script sees the object's fields as FIELDS and
// the extra arguments as ARGV. It accumulates: each call adds another filter.
func (cmd *WithinCmd[E]) WhereEval(script string, args ...any) *WithinCmd[E] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVAL", script, args)...)
	return cmd
}

// WhereEvalSha is WhereEval against a script already loaded on the server,
// matching Tile38's WHEREEVALSHA keyword.
func (cmd *WithinCmd[E]) WhereEvalSha(sha string, args ...any) *WithinCmd[E] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVALSHA", sha, args)...)
	return cmd
}

// Buffer grows the search area by the given number of metres before matching,
// matching Tile38's BUFFER keyword. Tile38 can only buffer point-like areas — it
// answers "cannot buffer Polygon type" for a Bounds or polygon Object area, and
// it panics rather than answering on NEARBY, which is why NearbyCmd has no
// Buffer.
//
// It is appended rather than stored: Tile38 has no duplicate guard for BUFFER,
// so a repeat is legal and the last one wins.
func (cmd *WithinCmd[E]) Buffer(metres int) *WithinCmd[E] {
	cmd.args = append(cmd.args, "BUFFER", metres)
	return cmd
}

// Get sets the search area to an object already stored in Tile38 (GET keyword).
func (cmd *WithinCmd[E]) Get(collection, id string) *WithinCmd[E] {
	cmd.geom = []any{"GET", collection, id}
	return cmd
}

// Object sets the search area to an inline GeoJSON string (OBJECT keyword).
func (cmd *WithinCmd[E]) Object(geojson string) *WithinCmd[E] {
	cmd.geom = []any{"OBJECT", geojson}
	return cmd
}

// Bounds sets the search area to a lat/lon bounding box (BOUNDS keyword).
func (cmd *WithinCmd[E]) Bounds(swLat, swLon, neLat, neLon float64) *WithinCmd[E] {
	cmd.geom = []any{"BOUNDS", swLat, swLon, neLat, neLon}
	return cmd
}

// Circle sets the search area to a circle with centre + radius in metres (CIRCLE keyword).
func (cmd *WithinCmd[E]) Circle(lat, lon float64, radius int) *WithinCmd[E] {
	cmd.geom = []any{"CIRCLE", lat, lon, radius}
	return cmd
}

// A5 sets the search area to a single A5 cell's pentagon, identified by its hex
// cell id (A5 keyword). Requires a server built from upstream master: A5 is
// merged upstream but has shipped in no release tag as of 1.38.0. Tile38 accepts
// A5 as a search area only, not as a hook or channel fence area.
func (cmd *WithinCmd[E]) A5(cellID string) *WithinCmd[E] {
	cmd.geom = []any{"A5", cellID}
	return cmd
}

// Sector sets the search area to a circular sector: a circle of radius metres
// centred on lat/lon, clipped to the arc between two compass bearings in
// degrees. Matches Tile38's SECTOR keyword, which NEARBY does not accept.
func (cmd *WithinCmd[E]) Sector(lat, lon float64, metres int, bearing1, bearing2 float64) *WithinCmd[E] {
	cmd.geom = []any{"SECTOR", lat, lon, metres, bearing1, bearing2}
	return cmd
}

// Hash sets the search area to the box a geohash covers, matching Tile38's HASH
// keyword. The shorter the hash, the larger the box.
func (cmd *WithinCmd[E]) Hash(geohash string) *WithinCmd[E] {
	cmd.geom = []any{"HASH", geohash}
	return cmd
}

// QuadKey sets the search area to the tile a Bing Maps quadkey names, matching
// Tile38's QUADKEY keyword. Tile is the same area expressed as x/y/z.
func (cmd *WithinCmd[E]) QuadKey(quadkey string) *WithinCmd[E] {
	cmd.geom = []any{"QUADKEY", quadkey}
	return cmd
}

// Tile sets the search area to a single XYZ map tile (TILE keyword).
func (cmd *WithinCmd[E]) Tile(x, y, z int) *WithinCmd[E] {
	cmd.geom = []any{"TILE", x, y, z}
	return cmd
}

// IDs selects the IDS output format: WITHIN collection [opts] IDS <area>.
// It is what a fresh command already emits, and is here to switch back.
func (cmd *WithinCmd[E]) IDs() *WithinCmd[string] {
	return &WithinCmd[string]{cmd.clone(), formatIDs}
}

// Points selects the POINTS output format: WITHIN collection [opts] POINTS <area>.
func (cmd *WithinCmd[E]) Points() *WithinCmd[NearbyResult] {
	return &WithinCmd[NearbyResult]{cmd.clone(), formatPoints}
}

// Objects selects the OBJECTS output format: WITHIN collection [opts] OBJECTS <area>.
func (cmd *WithinCmd[E]) Objects() *WithinCmd[SearchObject] {
	return &WithinCmd[SearchObject]{cmd.clone(), formatObjects}
}

// Rects selects the BOUNDS output format: WITHIN collection [opts] BOUNDS <area>.
// Each result is the bounding box of a matching object, lat first.
func (cmd *WithinCmd[E]) Rects() *WithinCmd[RectResult] {
	return &WithinCmd[RectResult]{cmd.clone(), formatRects}
}

// Hashes selects the HASHES output format: WITHIN collection [opts] HASHES precision <area>.
// Each result is the geohash of a matching object's centre.
func (cmd *WithinCmd[E]) Hashes(precision int) *WithinCmd[HashResult] {
	return &WithinCmd[HashResult]{cmd.clone(), formatHashes(precision)}
}

// A5Cells selects the A5 output format: WITHIN collection [opts] A5 level <area>.
// Requires a server built from upstream master.
func (cmd *WithinCmd[E]) A5Cells(level int) *WithinCmd[A5Result] {
	return &WithinCmd[A5Result]{cmd.clone(), formatA5Cells(level)}
}

// Do executes the command in whichever output format was selected, defaulting
// to IDS. It is one round trip and returns one page.
//
// Tile38 caps every output except COUNT at 100 results when the command carries
// no LIMIT (limitItems, internal/server/scanner.go), so a query that is complete
// against a small collection quietly returns a prefix once that collection
// grows. Truncation is not an error: NextCursor is non-zero when the server
// stopped early, and Iter pages past the cap instead.
func (cmd *WithinCmd[E]) Do(ctx context.Context) ([]E, error) {
	return searchDo(ctx, cmd.searchState, cmd.out)
}

// Iter pages the search to completion, yielding one result at a time in whichever
// output format was selected, following the cursor itself so the hundred-result
// cap never truncates what the caller sees.
//
//	for obj, err := range cmd.Objects().Iter(ctx) {
//		if err != nil {
//			return err
//		}
//		use(obj)
//	}
//
// An explicit Limit or Cursor is the caller's own bound, so Iter yields that one
// page rather than paging past it. Breaking out of the range just stops asking
// for pages; nothing is left open.
func (cmd *WithinCmd[E]) Iter(ctx context.Context) iter.Seq2[E, error] {
	return searchIter(ctx, cmd.searchState, cmd.out)
}

// Count runs the COUNT form: WITHIN collection [opts] COUNT <area>.
// It returns the number of matching objects.
//
// COUNT is a terminal rather than an output format: its reply is a bare
// integer, so there is no element type for a builder to carry, and the
// hundred-result cap does not apply: the server exempts COUNT from it.
func (cmd *WithinCmd[E]) Count(ctx context.Context) (int, error) {
	return searchCount(ctx, cmd.searchState)
}

// Fence opens a live geofence: WITHIN collection [opts] FENCE [DETECT …] <area>.
// The returned Stream holds a dedicated connection and delivers events until it
// is closed or ctx is cancelled.
func (cmd *WithinCmd[E]) Fence(ctx context.Context) (*Stream, error) {
	return cmd.c.fenceStream(ctx, cmd.fenceArgs())
}

// DelHookCmd builds a Tile38 DELHOOK command.
type DelHookCmd struct {
	c    *Client
	args []any
}

// Do executes: DELHOOK hookName
func (cmd *DelHookCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: DELHOOK: %w", err)
	}
	return nil
}

// PDelHookCmd builds a Tile38 PDELHOOK command (pattern-based hook deletion).
type PDelHookCmd struct {
	c    *Client
	args []any
}

// Do executes: PDELHOOK pattern
func (cmd *PDelHookCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: PDELHOOK: %w", err)
	}
	return nil
}

// HooksCmd builds a Tile38 HOOKS command to list registered hooks.
type HooksCmd struct {
	c    *Client
	args []any
}

// Do executes: HOOKS [pattern]
func (cmd *HooksCmd) Do(ctx context.Context) ([]HookInfo, error) {
	val, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return nil, fmt.Errorf("tile38: HOOKS: %w", err)
	}
	return parseHooks("HOOKS", val)
}

// ── Fences: SETHOOK and SETCHAN ───────────────────────────────────────────────

// fenceState is everything SETHOOK and SETCHAN hold. The two commands take the
// same trigger grammar after their name, so the parts — and the protocol order
// they assemble in — are identical; only SETHOOK's positional endpoints differ.
type fenceState struct {
	c        *Client
	name     string
	meta     [][2]string
	ex       *int
	trigger  []any // NEARBY|WITHIN|INTERSECTS collection
	args     []any // repeatable options that follow the trigger
	detect   []DetectState
	commands []Command
	nodwell  bool
	distance bool
	geom     []any // fence area
	radius   *int  // trailing metres of a POINT area
}

// fenceBase carries the chain methods HookCmd and SetChanCmd share. Self is the
// concrete builder, so a chained method returns that rather than the base and
// HookCmd.Endpoint stays reachable mid-chain.
type fenceBase[Self any] struct {
	fenceState
	self Self
}

// Nearby selects the NEARBY spatial trigger. Use with Point and Radius, or with
// Roam.
func (cmd *fenceBase[Self]) Nearby(collection string) Self {
	cmd.trigger = []any{"NEARBY", collection}
	return cmd.self
}

// Within selects the WITHIN spatial trigger. Use with any fence area.
func (cmd *fenceBase[Self]) Within(collection string) Self {
	cmd.trigger = []any{"WITHIN", collection}
	return cmd.self
}

// Intersects selects the INTERSECTS spatial trigger, which fires on any overlap
// with the fence area rather than requiring full containment.
func (cmd *fenceBase[Self]) Intersects(collection string) Self {
	cmd.trigger = []any{"INTERSECTS", collection}
	return cmd.self
}

// Meta attaches a key/value pair to the fence, echoed back on every event it
// produces. It accumulates: each call adds another pair.
func (cmd *fenceBase[Self]) Meta(key, value string) Self {
	cmd.meta = append(cmd.meta, [2]string{key, value})
	return cmd.self
}

// EX sets how long the fence lives before Tile38 removes it, in seconds.
func (cmd *fenceBase[Self]) EX(secs int) Self {
	cmd.ex = &secs
	return cmd.self
}

// Where sets an optional Tile38 field expression filter.
func (cmd *fenceBase[Self]) Where(expr string) Self {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd.self
}

// Detect restricts the fence to the given transitions. When omitted, Tile38's
// default detect set applies.
func (cmd *fenceBase[Self]) Detect(states ...DetectState) Self {
	cmd.detect = states
	return cmd.self
}

// Commands restricts the fence to events caused by the given commands.
func (cmd *fenceBase[Self]) Commands(commands ...Command) Self {
	cmd.commands = commands
	return cmd.self
}

// Roam fires when objects in the trigger collection come within radiusM metres
// of an object in collection. Use with Nearby.
//
// Objects that stay in range keep reporting on each update; chain NoDwell to
// suppress those.
func (cmd *fenceBase[Self]) Roam(collection string, radiusM int) Self {
	cmd.geom = []any{"ROAM", collection, "*", radiusM}
	return cmd.self
}

// NoDwell stops a roaming fence from re-reporting objects that stay within range
// between updates, matching Tile38's NODWELL keyword. It only affects Roam
// fences, and it is opt-in: dwelling is Tile38's own default.
func (cmd *fenceBase[Self]) NoDwell() Self {
	cmd.nodwell = true
	return cmd.self
}

// Distance adds each object's distance from the fence centre to every event the
// fence produces, matching Tile38's DISTANCE keyword. It arrives on FenceEvent
// as Distance, and applies to the live fence only — a plain query reads the same
// value through PointsWithDistance.
func (cmd *fenceBase[Self]) Distance() Self {
	cmd.distance = true
	return cmd.self
}

// Bounds sets the fence area to a lat/lon bounding box. Pass GlobalBounds() to
// fence the whole world.
func (cmd *fenceBase[Self]) Bounds(swLat, swLon, neLat, neLon float64) Self {
	cmd.geom = []any{"BOUNDS", swLat, swLon, neLat, neLon}
	return cmd.self
}

// Circle sets the fence area to a circle with centre + radius in metres.
func (cmd *fenceBase[Self]) Circle(lat, lon float64, radius int) Self {
	cmd.geom = []any{"CIRCLE", lat, lon, radius}
	return cmd.self
}

// Point sets the fence area to a point, and is the area a Nearby trigger takes:
// NEARBY reads "POINT lat lon meters" and rejects CIRCLE, so a hook or channel
// fencing on NEARBY needs this rather than Circle. Pair it with Radius.
func (cmd *fenceBase[Self]) Point(lat, lon float64) Self {
	cmd.geom = []any{"POINT", lat, lon}
	return cmd.self
}

// Radius sets the trailing metres of a Point area. Named for the value it
// carries: Tile38 has no keyword for it, it is the last argument of
// "POINT lat lon meters".
func (cmd *fenceBase[Self]) Radius(metres int) Self {
	cmd.radius = &metres
	return cmd.self
}

// Object sets the fence area to an inline GeoJSON string.
func (cmd *fenceBase[Self]) Object(geojson string) Self {
	cmd.geom = []any{"OBJECT", geojson}
	return cmd.self
}

// Sector sets the search area to a circular sector: a circle of radius metres
// centred on lat/lon, clipped to the arc between two compass bearings in
// degrees. Matches Tile38's SECTOR keyword, which NEARBY does not accept.
func (cmd *fenceBase[Self]) Sector(lat, lon float64, metres int, bearing1, bearing2 float64) Self {
	cmd.geom = []any{"SECTOR", lat, lon, metres, bearing1, bearing2}
	return cmd.self
}

// Hash sets the search area to the box a geohash covers, matching Tile38's HASH
// keyword. The shorter the hash, the larger the box.
func (cmd *fenceBase[Self]) Hash(geohash string) Self {
	cmd.geom = []any{"HASH", geohash}
	return cmd.self
}

// QuadKey sets the search area to the tile a Bing Maps quadkey names, matching
// Tile38's QUADKEY keyword. Tile is the same area expressed as x/y/z.
func (cmd *fenceBase[Self]) QuadKey(quadkey string) Self {
	cmd.geom = []any{"QUADKEY", quadkey}
	return cmd.self
}

// Get sets the fence area to an object already stored in Tile38.
func (cmd *fenceBase[Self]) Get(collection, id string) Self {
	cmd.geom = []any{"GET", collection, id}
	return cmd.self
}

// buildFence assembles the trigger, options, fence clause and area that follow a
// SETHOOK or SETCHAN head, in the protocol order Tile38's parser requires.
func (cmd *fenceBase[Self]) buildFence(head []any) []any {
	head = append(head, cmd.trigger...)
	return buildSearch(append(head, cmd.args...), searchOpts{},
		fenceTokens(cmd.distance, cmd.detect, cmd.commands, cmd.nodwell), nil,
		pointGeometry(cmd.geom, cmd.radius))
}

// HookCmd builds a Tile38 SETHOOK command: an endpoint, a spatial trigger
// (Nearby/Within), optional Detect/Commands filters, and one fence area
// (Bounds/Circle/Object/Get, or Roam). It shares its chain methods with
// SetChanCmd through fenceBase, so every one of them returns *HookCmd however
// godoc renders the promoted signature.
//
// Methods may be chained in any order; the parts are assembled into protocol
// order when the command runs.
type HookCmd struct {
	fenceBase[*HookCmd]
	endpoints []string
}

// Endpoint adds a target endpoint built by joining a base URL and a subject or
// path with "/" — the shape NATS and HTTP endpoints take
// (e.g. "nats://host:4222" + "subject"). For any other scheme, or for a URL
// carrying query parameters, use EndpointURL.
//
// Calling Endpoint or EndpointURL more than once registers every endpoint on the
// hook; Tile38 delivers each event to all of them.
func (cmd *HookCmd) Endpoint(baseURL, subject string) *HookCmd {
	return cmd.EndpointURL(baseURL + "/" + subject)
}

// EndpointURL adds target endpoints verbatim, for schemes whose URL is not a
// base plus a path — kafka://host:9092/topic, sqs://region/queue,
// grpc://host:port, or an http:// URL with a query string.
func (cmd *HookCmd) EndpointURL(urls ...string) *HookCmd {
	cmd.endpoints = append(cmd.endpoints, urls...)
	return cmd
}

// Match filters the trigger collection by object ID pattern (glob-style, e.g.
// "org:*"), matching Tile38's MATCH keyword. It accumulates: each call adds
// another pattern.
func (cmd *HookCmd) Match(pattern string) *HookCmd {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// Do executes the SETHOOK command.
func (cmd *HookCmd) Do(ctx context.Context) error {
	head := hookHead([]any{"SETHOOK", cmd.name, strings.Join(cmd.endpoints, ",")}, cmd.meta, cmd.ex)
	if _, err := cmd.c.do(ctx, cmd.buildFence(head)...); err != nil {
		return fmt.Errorf("tile38: SETHOOK: %w", err)
	}
	return nil
}

// ── Pipeline ──────────────────────────────────────────────────────────────────

// Pipeline batches SET commands and executes them in a single round trip.
// It is not safe for concurrent use.
type Pipeline struct {
	c    *Client
	cmds [][]any
}

// Pipeline returns a Pipeline for batching multiple SET commands in one round trip.
func (c *Client) Pipeline() *Pipeline {
	return &Pipeline{c: c}
}

// Set returns a PipelineSetCmd. Chain modifiers then call Queue to enqueue.
func (p *Pipeline) Set(collection, id string) *PipelineSetCmd {
	return &PipelineSetCmd{p: p, args: []any{"SET", collection, id}}
}

// Len reports how many commands are queued.
func (p *Pipeline) Len() int { return len(p.cmds) }

// Flush writes all queued commands in one batch, reads every reply, and resets
// the pipeline. It returns the first command error encountered.
func (p *Pipeline) Flush(ctx context.Context) error {
	if len(p.cmds) == 0 {
		return nil
	}
	cmds := p.cmds
	p.cmds = nil
	if err := p.c.doPipeline(ctx, cmds); err != nil {
		return fmt.Errorf("tile38: pipeline: %w", err)
	}
	return nil
}

// PipelineSetCmd is a deferred SET command queued onto a Pipeline.
// Chain modifiers then call Queue to enqueue.
type PipelineSetCmd struct {
	p    *Pipeline
	args []any
}

// EX sets the expiry in seconds, matching Tile38's EX keyword. Zero means no expiry.
func (cmd *PipelineSetCmd) EX(secs int) *PipelineSetCmd {
	if secs > 0 {
		cmd.args = append(cmd.args, "EX", secs)
	}
	return cmd
}

// Field appends a single named field to the SET command.
func (cmd *PipelineSetCmd) Field(name string, value any) *PipelineSetCmd {
	cmd.args = append(cmd.args, "FIELD", name, value)
	return cmd
}

// Fields appends multiple named fields to the SET command in one call.
func (cmd *PipelineSetCmd) Fields(fields ...field) *PipelineSetCmd {
	for _, f := range fields {
		cmd.args = append(cmd.args, "FIELD", f.name, f.value)
	}
	return cmd
}

// PointZ sets the POINT coordinates with a third ordinate. See SetCmd.PointZ.
func (cmd *PipelineSetCmd) PointZ(lat, lon, z float64) *PipelineSetCmd {
	cmd.args = append(cmd.args, "POINT", lat, lon, z)
	return cmd
}

// Point sets the POINT coordinates.
func (cmd *PipelineSetCmd) Point(lat, lon float64) *PipelineSetCmd {
	cmd.args = append(cmd.args, "POINT", lat, lon)
	return cmd
}

// Queue enqueues the command onto the pipeline. Context is supplied at Flush.
func (cmd *PipelineSetCmd) Queue() {
	cmd.p.cmds = append(cmd.p.cmds, cmd.args)
}
