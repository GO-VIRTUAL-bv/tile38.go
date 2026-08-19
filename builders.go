// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"context"
	"fmt"
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

// truncation reports what a terminal should return alongside its results. A
// non-zero cursor means Tile38 stopped at the limit, which is only surprising
// when the caller never asked for a bound — an explicit Limit or Cursor means
// they did, so it stays silent there.
func truncation(o searchOpts, cursor uint64) error {
	if cursor == 0 || o.limit != nil || o.cursor != nil {
		return nil
	}
	return ErrTruncated
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

// searchOutput couples an output format's protocol tokens with the parser for
// the reply shape it produces. A type parameter carries no behaviour at run
// time, so the parse func has to ride alongside it — that is what lets a single
// Do serve every output format instead of one terminal per format per builder.
type searchOutput[T any] struct {
	name   string // the format's keyword, for error text
	tokens []string
	parse  func(any) ([]T, uint64, error)
}

// searchDo runs the assembled command and decodes it with the output's parser.
// It is a free function rather than a method because the builders instantiate
// searchOutput at several different T within one chain.
func searchDo[T any](ctx context.Context, s *searchState, out searchOutput[T]) ([]T, error) {
	val, err := s.c.do(ctx, s.execArgs(out.tokens)...)
	if err != nil {
		return nil, fmt.Errorf("tile38: %s %s: %w", s.verb, out.name, err)
	}
	res, cursor, err := out.parse(val)
	if err != nil {
		return nil, err
	}
	s.cursorOut = cursor
	return res, truncation(s.opts, cursor)
}

// The output constructors below are shared by every builder that accepts the
// format. Each binds the verb into the parser's error prefix, which is the only
// thing that differed between the old per-builder terminals.

func outIDs() searchOutput[string] {
	return searchOutput[string]{"IDS", []string{"IDS"}, parseScanIDs}
}

// outPoints picks the parser by verb only to keep the existing error prefixes:
// the two differ in nothing else.
func outPoints(verb string) searchOutput[NearbyResult] {
	parse := parseNearbyPoints
	if verb == "SCAN" {
		parse = parseScanPoints
	}
	return searchOutput[NearbyResult]{"POINTS", []string{"POINTS"}, parse}
}

// outPointsWithDistance selects POINTS with the DISTANCE option in front of it.
// DISTANCE is an option token, so it has to precede the output format.
func outPointsWithDistance() searchOutput[NearbyResultWithDistance] {
	return searchOutput[NearbyResultWithDistance]{
		"DISTANCE POINTS", []string{"DISTANCE", "POINTS"}, parsePointsWithDistance,
	}
}

func outObjects(verb string) searchOutput[SearchObject] {
	return searchOutput[SearchObject]{"OBJECTS", []string{"OBJECTS"},
		func(v any) ([]SearchObject, uint64, error) { return parseObjects(verb, v) }}
}

func outRects(verb string) searchOutput[RectResult] {
	return searchOutput[RectResult]{"BOUNDS", []string{"BOUNDS"},
		func(v any) ([]RectResult, uint64, error) { return parseRects(verb, v) }}
}

func outHashes(verb string, precision int) searchOutput[HashResult] {
	return searchOutput[HashResult]{"HASHES", []string{"HASHES", strconv.Itoa(precision)},
		func(v any) ([]HashResult, uint64, error) { return parseHashes(verb, v) }}
}

func outA5Cells(verb string, level int) searchOutput[A5Result] {
	return searchOutput[A5Result]{"A5", []string{"A5", strconv.Itoa(level)},
		func(v any) ([]A5Result, uint64, error) { return parseA5Cells(verb, v) }}
}

func outStrings(verb string) searchOutput[StringObject] {
	return searchOutput[StringObject]{"STRINGS", nil,
		func(v any) ([]StringObject, uint64, error) { return parseStrings(verb, v) }}
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
	if err != nil || !cmd.withFields || val == nil {
		return val, err
	}
	outer, ok := val.([]any)
	if !ok || len(outer) == 0 {
		return nil, fmt.Errorf("tile38: GET WITHFIELDS: unexpected response shape: %T", val)
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
		return "", fmt.Errorf("tile38: GET: unexpected response type %T", val)
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
		return "", fmt.Errorf("tile38: GET HASH: unexpected response type %T", val)
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
		return "", fmt.Errorf("tile38: GET A5: unexpected response type %T", val)
	}
	return s, nil
}

// ── Spatial search ────────────────────────────────────────────────────────────

// NearbyCmd builds a Tile38 NEARBY command. Methods may be chained in any
// order; the parts are assembled into protocol order when the command runs.
//
// The type parameter is the result type Do returns. A fresh command is
// NearbyCmd[string] and emits IDS; an output-format method — Points, Objects,
// Rects, Hashes, A5Cells, PointsWithDistance — hands back a command at the
// matching type. Options chain the same either side of that switch.
type NearbyCmd[T any] struct {
	*searchState
	out searchOutput[T]
}

// nearbyAt re-wraps the chain at a new output type. The state is cloned so the
// handle before the switch and the one after it cannot mutate each other.
func nearbyAt[T, U any](cmd *NearbyCmd[T], out searchOutput[U]) *NearbyCmd[U] {
	return &NearbyCmd[U]{cmd.clone(), out}
}

// Limit caps the number of results. Zero means no limit.
func (cmd *NearbyCmd[T]) Limit(n int) *NearbyCmd[T] {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a search from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported. Setting it also means the
// caller is paging deliberately, so a truncated result no longer reports
// ErrTruncated. Tile38 rejects CURSOR on a fence, so Fence ignores it.
func (cmd *NearbyCmd[T]) Cursor(n uint64) *NearbyCmd[T] {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed Do. It is non-zero
// only when Tile38 stopped at the limit with more objects matching.
func (cmd *NearbyCmd[T]) NextCursor() uint64 { return cmd.cursorOut }

// Where sets an optional Tile38 field expression filter.
func (cmd *NearbyCmd[T]) Where(expr string) *NearbyCmd[T] {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// Match filters results by ID pattern (glob-style, e.g. "truck:*").
func (cmd *NearbyCmd[T]) Match(pattern string) *NearbyCmd[T] {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *NearbyCmd[T]) WhereIn(field string, values ...any) *NearbyCmd[T] {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
func (cmd *NearbyCmd[T]) NoFields() *NearbyCmd[T] {
	cmd.opts.nofields = true
	return cmd
}

// Sparse spreads results evenly over the search area at the given depth (1-8),
// matching Tile38's SPARSE keyword. Tile38 rejects SPARSE combined with Limit.
func (cmd *NearbyCmd[T]) Sparse(depth int) *NearbyCmd[T] {
	cmd.opts.sparse = &depth
	return cmd
}

// Detect restricts a live fence to the given transitions. Only meaningful with Fence.
func (cmd *NearbyCmd[T]) Detect(states ...DetectState) *NearbyCmd[T] {
	cmd.detect = states
	return cmd
}

// Commands restricts a live fence to events caused by the given commands.
// Only meaningful with Fence.
func (cmd *NearbyCmd[T]) Commands(commands ...Command) *NearbyCmd[T] {
	cmd.commands = commands
	return cmd
}

// Distance adds each object's distance from the fence centre to every event the
// fence produces, matching Tile38's DISTANCE keyword. It arrives on FenceEvent
// as Distance, and applies to the live fence only — a plain query reads the same
// value through PointsWithDistance.
func (cmd *NearbyCmd[T]) Distance() *NearbyCmd[T] {
	cmd.distance = true
	return cmd
}

// WhereEval keeps results for which the given Lua script returns true, matching
// Tile38's WHEREEVAL keyword. The script sees the object's fields as FIELDS and
// the extra arguments as ARGV. It accumulates: each call adds another filter.
func (cmd *NearbyCmd[T]) WhereEval(script string, args ...any) *NearbyCmd[T] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVAL", script, args)...)
	return cmd
}

// WhereEvalSha is WhereEval against a script already loaded on the server,
// matching Tile38's WHEREEVALSHA keyword.
func (cmd *NearbyCmd[T]) WhereEvalSha(sha string, args ...any) *NearbyCmd[T] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVALSHA", sha, args)...)
	return cmd
}

// Point sets the centre coordinates.
func (cmd *NearbyCmd[T]) Point(lat, lon float64) *NearbyCmd[T] {
	cmd.geom = []any{"POINT", lat, lon}
	return cmd
}

// Radius sets the search radius in metres. It applies to a Point area; a Roam
// area carries its own radius.
func (cmd *NearbyCmd[T]) Radius(metres int) *NearbyCmd[T] {
	cmd.radius = &metres
	return cmd
}

// Roam turns the fence into a roaming one: it fires as objects in this
// collection move within radiusM metres of an object in collection. Tile38
// accepts ROAM only on a live NEARBY fence, so this needs Fence — Do will be
// rejected by the server.
func (cmd *NearbyCmd[T]) Roam(collection string, radiusM int) *NearbyCmd[T] {
	cmd.geom = []any{"ROAM", collection, "*", radiusM}
	return cmd
}

// NoDwell stops a roaming fence from re-reporting objects that stay within
// range between updates, matching Tile38's NODWELL keyword. It only affects
// Roam fences.
func (cmd *NearbyCmd[T]) NoDwell() *NearbyCmd[T] {
	cmd.nodwell = true
	return cmd
}

// IDs selects the IDS output format: NEARBY collection [opts] IDS POINT lat lon radius.
// It is what a fresh command already emits, and is here to switch back.
func (cmd *NearbyCmd[T]) IDs() *NearbyCmd[string] { return nearbyAt(cmd, outIDs()) }

// Points selects the POINTS output format: NEARBY collection [opts] POINTS POINT lat lon radius.
func (cmd *NearbyCmd[T]) Points() *NearbyCmd[NearbyResult] {
	return nearbyAt(cmd, outPoints("NEARBY"))
}

// PointsWithDistance selects POINTS with each object's distance from the search
// centre: NEARBY collection [opts] DISTANCE POINTS POINT lat lon radius.
// DISTANCE is an option token, so it has to precede the output format.
func (cmd *NearbyCmd[T]) PointsWithDistance() *NearbyCmd[NearbyResultWithDistance] {
	return nearbyAt(cmd, outPointsWithDistance())
}

// Objects selects the OBJECTS output format: NEARBY collection [opts] OBJECTS POINT lat lon radius.
func (cmd *NearbyCmd[T]) Objects() *NearbyCmd[SearchObject] {
	return nearbyAt(cmd, outObjects("NEARBY"))
}

// Rects selects the BOUNDS output format: NEARBY collection [opts] BOUNDS POINT lat lon radius.
// Each result is the bounding box of a matching object, lat first.
func (cmd *NearbyCmd[T]) Rects() *NearbyCmd[RectResult] {
	return nearbyAt(cmd, outRects("NEARBY"))
}

// Hashes selects the HASHES output format: NEARBY collection [opts] HASHES precision POINT lat lon radius.
// Each result is the geohash of a matching object's centre.
func (cmd *NearbyCmd[T]) Hashes(precision int) *NearbyCmd[HashResult] {
	return nearbyAt(cmd, outHashes("NEARBY", precision))
}

// A5Cells selects the A5 output format: NEARBY collection [opts] A5 level POINT lat lon radius.
// Each result is the A5 cell a matching object's centre falls in. Named for the
// output rather than the keyword because A5 is already the search-area method on
// the builders that take one. Requires a server built from upstream master.
func (cmd *NearbyCmd[T]) A5Cells(level int) *NearbyCmd[A5Result] {
	return nearbyAt(cmd, outA5Cells("NEARBY", level))
}

// Do executes the command in whichever output format was selected, defaulting
// to IDS. It reports ErrTruncated when Tile38 capped an unbounded search.
func (cmd *NearbyCmd[T]) Do(ctx context.Context) ([]T, error) {
	return searchDo(ctx, cmd.searchState, cmd.out)
}

// Count executes: NEARBY collection [opts] COUNT POINT lat lon radius.
// It is not part of the Do path: COUNT is exempt from Tile38's result cap and
// replies with a bare integer rather than a cursor and a list.
func (cmd *NearbyCmd[T]) Count(ctx context.Context) (int, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs([]string{"COUNT"})...)
	if err != nil {
		return 0, fmt.Errorf("tile38: NEARBY COUNT: %w", err)
	}
	return parseCount("NEARBY", val)
}

// Fence opens a live geofence: NEARBY collection [opts] FENCE [DETECT …] POINT lat lon radius.
// The returned Stream holds a dedicated connection and delivers events until it
// is closed or ctx is cancelled.
func (cmd *NearbyCmd[T]) Fence(ctx context.Context) (*Stream, error) {
	return cmd.c.fenceStream(ctx, cmd.fenceArgs())
}

// ScanCmd builds a Tile38 SCAN command.
//
// The type parameter is the result type Do returns; see NearbyCmd. SCAN takes
// no search area and no fence.
type ScanCmd[T any] struct {
	*searchState
	out searchOutput[T]
}

func scanAt[T, U any](cmd *ScanCmd[T], out searchOutput[U]) *ScanCmd[U] {
	return &ScanCmd[U]{cmd.clone(), out}
}

// Limit caps the number of results. Zero means no limit.
func (cmd *ScanCmd[T]) Limit(n int) *ScanCmd[T] {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a scan from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported. Setting it also means the
// caller is paging deliberately, so a truncated result no longer reports
// ErrTruncated.
func (cmd *ScanCmd[T]) Cursor(n uint64) *ScanCmd[T] {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed Do. It is non-zero
// only when Tile38 stopped at the limit with more objects matching.
func (cmd *ScanCmd[T]) NextCursor() uint64 { return cmd.cursorOut }

// Match filters results by ID pattern (glob-style, e.g. "truck:*").
func (cmd *ScanCmd[T]) Match(pattern string) *ScanCmd[T] {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// Where sets an optional Tile38 field expression filter.
func (cmd *ScanCmd[T]) Where(expr string) *ScanCmd[T] {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *ScanCmd[T]) WhereIn(field string, values ...any) *ScanCmd[T] {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
func (cmd *ScanCmd[T]) NoFields() *ScanCmd[T] {
	cmd.opts.nofields = true
	return cmd
}

// Asc returns results in ascending ID order, matching Tile38's ASC keyword.
// Only SCAN and SEARCH take an order — the spatial verbs answer
// "ASC is not allowed for NEARBY". Asc and Desc overwrite each other: Tile38
// rejects a command carrying both.
func (cmd *ScanCmd[T]) Asc() *ScanCmd[T] {
	order := "ASC"
	cmd.opts.order = &order
	return cmd
}

// Desc returns results in descending ID order, matching Tile38's DESC keyword.
// See Asc for why it is single-use.
func (cmd *ScanCmd[T]) Desc() *ScanCmd[T] {
	order := "DESC"
	cmd.opts.order = &order
	return cmd
}

// WhereEval keeps results for which the given Lua script returns true, matching
// Tile38's WHEREEVAL keyword. The script sees the object's fields as FIELDS and
// the extra arguments as ARGV. It accumulates: each call adds another filter.
func (cmd *ScanCmd[T]) WhereEval(script string, args ...any) *ScanCmd[T] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVAL", script, args)...)
	return cmd
}

// WhereEvalSha is WhereEval against a script already loaded on the server,
// matching Tile38's WHEREEVALSHA keyword.
func (cmd *ScanCmd[T]) WhereEvalSha(sha string, args ...any) *ScanCmd[T] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVALSHA", sha, args)...)
	return cmd
}

// IDs selects the IDS output format: SCAN collection [opts] IDS.
// It is what a fresh command already emits, and is here to switch back.
func (cmd *ScanCmd[T]) IDs() *ScanCmd[string] { return scanAt(cmd, outIDs()) }

// Points selects the POINTS output format: SCAN collection [opts] POINTS.
func (cmd *ScanCmd[T]) Points() *ScanCmd[NearbyResult] {
	return scanAt(cmd, outPoints("SCAN"))
}

// Objects selects the OBJECTS output format: SCAN collection [opts] OBJECTS.
func (cmd *ScanCmd[T]) Objects() *ScanCmd[SearchObject] {
	return scanAt(cmd, outObjects("SCAN"))
}

// Rects selects the BOUNDS output format: SCAN collection [opts] BOUNDS.
// Each result is the bounding box of a matching object, lat first.
func (cmd *ScanCmd[T]) Rects() *ScanCmd[RectResult] {
	return scanAt(cmd, outRects("SCAN"))
}

// Hashes selects the HASHES output format: SCAN collection [opts] HASHES precision.
// Each result is the geohash of a matching object's centre.
func (cmd *ScanCmd[T]) Hashes(precision int) *ScanCmd[HashResult] {
	return scanAt(cmd, outHashes("SCAN", precision))
}

// A5Cells selects the A5 output format: SCAN collection [opts] A5 level.
// Requires a server built from upstream master.
func (cmd *ScanCmd[T]) A5Cells(level int) *ScanCmd[A5Result] {
	return scanAt(cmd, outA5Cells("SCAN", level))
}

// Do executes the command in whichever output format was selected, defaulting
// to IDS. It reports ErrTruncated when Tile38 capped an unbounded scan.
func (cmd *ScanCmd[T]) Do(ctx context.Context) ([]T, error) {
	return searchDo(ctx, cmd.searchState, cmd.out)
}

// Count executes: SCAN collection [opts] COUNT.
// It is not part of the Do path: COUNT is exempt from Tile38's result cap and
// replies with a bare integer rather than a cursor and a list.
func (cmd *ScanCmd[T]) Count(ctx context.Context) (int, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs([]string{"COUNT"})...)
	if err != nil {
		return 0, fmt.Errorf("tile38: SCAN COUNT: %w", err)
	}
	return parseCount("SCAN", val)
}

// SearchCmd builds a Tile38 SEARCH command, which matches on the string values
// "SET … STRING" stores rather than on geometry. It takes no area and no fence.
//
// The type parameter is the result type Do returns; see NearbyCmd. A fresh
// command emits IDS; Strings selects SEARCH's own default output, which carries
// each object's string value.
type SearchCmd[T any] struct {
	*searchState
	out searchOutput[T]
}

func searchAt[T, U any](cmd *SearchCmd[T], out searchOutput[U]) *SearchCmd[U] {
	return &SearchCmd[U]{cmd.clone(), out}
}

// Limit caps the number of results. Zero means no limit.
func (cmd *SearchCmd[T]) Limit(n int) *SearchCmd[T] {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a search from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported.
func (cmd *SearchCmd[T]) Cursor(n uint64) *SearchCmd[T] {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed Do.
func (cmd *SearchCmd[T]) NextCursor() uint64 { return cmd.cursorOut }

// Match filters results by the object's string value (glob-style).
func (cmd *SearchCmd[T]) Match(pattern string) *SearchCmd[T] {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// Asc returns results in ascending order, matching Tile38's ASC keyword.
// Asc and Desc overwrite each other: Tile38 rejects a command carrying both.
func (cmd *SearchCmd[T]) Asc() *SearchCmd[T] {
	order := "ASC"
	cmd.opts.order = &order
	return cmd
}

// Desc returns results in descending order, matching Tile38's DESC keyword.
// See Asc for why it is single-use.
func (cmd *SearchCmd[T]) Desc() *SearchCmd[T] {
	order := "DESC"
	cmd.opts.order = &order
	return cmd
}

// Where sets an optional Tile38 field expression filter.
func (cmd *SearchCmd[T]) Where(expr string) *SearchCmd[T] {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *SearchCmd[T]) WhereIn(field string, values ...any) *SearchCmd[T] {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// WhereEval keeps results for which the given Lua script returns true, matching
// Tile38's WHEREEVAL keyword. It accumulates: each call adds another filter.
func (cmd *SearchCmd[T]) WhereEval(script string, args ...any) *SearchCmd[T] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVAL", script, args)...)
	return cmd
}

// WhereEvalSha is WhereEval against a script already loaded on the server,
// matching Tile38's WHEREEVALSHA keyword.
func (cmd *SearchCmd[T]) WhereEvalSha(sha string, args ...any) *SearchCmd[T] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVALSHA", sha, args)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
func (cmd *SearchCmd[T]) NoFields() *SearchCmd[T] {
	cmd.opts.nofields = true
	return cmd
}

// IDs selects the IDS output format: SEARCH collection [opts] IDS.
// It is what a fresh command already emits, and is here to switch back.
func (cmd *SearchCmd[T]) IDs() *SearchCmd[string] { return searchAt(cmd, outIDs()) }

// Strings selects SEARCH's own default output, where element 1 of each item is
// the object's string value rather than its geometry. It emits no format token,
// because that shape is what SEARCH returns when none is given.
func (cmd *SearchCmd[T]) Strings() *SearchCmd[StringObject] {
	return searchAt(cmd, outStrings("SEARCH"))
}

// Do executes the command in whichever output format was selected, defaulting
// to IDS. It reports ErrTruncated when Tile38 capped an unbounded search.
func (cmd *SearchCmd[T]) Do(ctx context.Context) ([]T, error) {
	return searchDo(ctx, cmd.searchState, cmd.out)
}

// Count executes: SEARCH collection [opts] COUNT.
// It is not part of the Do path: COUNT is exempt from Tile38's result cap and
// replies with a bare integer rather than a cursor and a list.
func (cmd *SearchCmd[T]) Count(ctx context.Context) (int, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs([]string{"COUNT"})...)
	if err != nil {
		return 0, fmt.Errorf("tile38: SEARCH COUNT: %w", err)
	}
	return parseCount("SEARCH", val)
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
type WithinCmd[T any] struct {
	*searchState
	out searchOutput[T]
}

// withinAt re-wraps the chain at a new output type. The state is cloned so the
// handle before the switch and the one after it cannot mutate each other.
func withinAt[T, U any](cmd *WithinCmd[T], out searchOutput[U]) *WithinCmd[U] {
	return &WithinCmd[U]{cmd.clone(), out}
}

// Limit caps the number of results. Zero means no limit.
func (cmd *WithinCmd[T]) Limit(n int) *WithinCmd[T] {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a search from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported. Setting it also means the
// caller is paging deliberately, so a truncated result no longer reports
// ErrTruncated. Tile38 rejects CURSOR on a fence, so Fence ignores it.
func (cmd *WithinCmd[T]) Cursor(n uint64) *WithinCmd[T] {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed terminal. It is
// non-zero only when Tile38 stopped at the limit with more objects matching.
func (cmd *WithinCmd[T]) NextCursor() uint64 { return cmd.cursorOut }

// Where sets an optional Tile38 field expression filter.
func (cmd *WithinCmd[T]) Where(expr string) *WithinCmd[T] {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// Match filters results by ID pattern (glob-style, e.g. "truck:*").
func (cmd *WithinCmd[T]) Match(pattern string) *WithinCmd[T] {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *WithinCmd[T]) WhereIn(field string, values ...any) *WithinCmd[T] {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
func (cmd *WithinCmd[T]) NoFields() *WithinCmd[T] {
	cmd.opts.nofields = true
	return cmd
}

// Clip trims returned objects to the search area rather than returning them
// whole, matching Tile38's CLIP keyword.
func (cmd *WithinCmd[T]) Clip() *WithinCmd[T] {
	cmd.opts.clip = true
	return cmd
}

// Sparse spreads results evenly over the search area at the given depth (1-8),
// matching Tile38's SPARSE keyword. Tile38 rejects SPARSE combined with Limit.
func (cmd *WithinCmd[T]) Sparse(depth int) *WithinCmd[T] {
	cmd.opts.sparse = &depth
	return cmd
}

// Detect restricts a live fence to the given transitions. Only meaningful with Fence.
func (cmd *WithinCmd[T]) Detect(states ...DetectState) *WithinCmd[T] {
	cmd.detect = states
	return cmd
}

// Commands restricts a live fence to events caused by the given commands.
// Only meaningful with Fence.
func (cmd *WithinCmd[T]) Commands(commands ...Command) *WithinCmd[T] {
	cmd.commands = commands
	return cmd
}

// Distance adds each object's distance from the fence centre to every event the
// fence produces, matching Tile38's DISTANCE keyword. It arrives on FenceEvent
// as Distance, and applies to the live fence only — a plain query reads the same
// value through PointsWithDistance.
func (cmd *WithinCmd[T]) Distance() *WithinCmd[T] {
	cmd.distance = true
	return cmd
}

// WhereEval keeps results for which the given Lua script returns true, matching
// Tile38's WHEREEVAL keyword. The script sees the object's fields as FIELDS and
// the extra arguments as ARGV. It accumulates: each call adds another filter.
func (cmd *WithinCmd[T]) WhereEval(script string, args ...any) *WithinCmd[T] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVAL", script, args)...)
	return cmd
}

// WhereEvalSha is WhereEval against a script already loaded on the server,
// matching Tile38's WHEREEVALSHA keyword.
func (cmd *WithinCmd[T]) WhereEvalSha(sha string, args ...any) *WithinCmd[T] {
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
func (cmd *WithinCmd[T]) Buffer(metres int) *WithinCmd[T] {
	cmd.args = append(cmd.args, "BUFFER", metres)
	return cmd
}

// Get sets the search area to an object already stored in Tile38 (GET keyword).
func (cmd *WithinCmd[T]) Get(collection, id string) *WithinCmd[T] {
	cmd.geom = []any{"GET", collection, id}
	return cmd
}

// Object sets the search area to an inline GeoJSON string (OBJECT keyword).
func (cmd *WithinCmd[T]) Object(geojson string) *WithinCmd[T] {
	cmd.geom = []any{"OBJECT", geojson}
	return cmd
}

// Bounds sets the search area to a lat/lon bounding box (BOUNDS keyword).
func (cmd *WithinCmd[T]) Bounds(swLat, swLon, neLat, neLon float64) *WithinCmd[T] {
	cmd.geom = []any{"BOUNDS", swLat, swLon, neLat, neLon}
	return cmd
}

// Circle sets the search area to a circle with centre + radius in metres (CIRCLE keyword).
func (cmd *WithinCmd[T]) Circle(lat, lon float64, radius int) *WithinCmd[T] {
	cmd.geom = []any{"CIRCLE", lat, lon, radius}
	return cmd
}

// A5 sets the search area to a single A5 cell's pentagon, identified by its hex
// cell id (A5 keyword). Requires a server built from upstream master: A5 is
// merged upstream but has shipped in no release tag as of 1.38.0. Tile38 accepts
// A5 as a search area only, not as a hook or channel fence area.
func (cmd *WithinCmd[T]) A5(cellID string) *WithinCmd[T] {
	cmd.geom = []any{"A5", cellID}
	return cmd
}

// Sector sets the search area to a circular sector: a circle of radius metres
// centred on lat/lon, clipped to the arc between two compass bearings in
// degrees. Matches Tile38's SECTOR keyword, which NEARBY does not accept.
func (cmd *WithinCmd[T]) Sector(lat, lon float64, metres int, bearing1, bearing2 float64) *WithinCmd[T] {
	cmd.geom = []any{"SECTOR", lat, lon, metres, bearing1, bearing2}
	return cmd
}

// Hash sets the search area to the box a geohash covers, matching Tile38's HASH
// keyword. The shorter the hash, the larger the box.
func (cmd *WithinCmd[T]) Hash(geohash string) *WithinCmd[T] {
	cmd.geom = []any{"HASH", geohash}
	return cmd
}

// QuadKey sets the search area to the tile a Bing Maps quadkey names, matching
// Tile38's QUADKEY keyword. Tile is the same area expressed as x/y/z.
func (cmd *WithinCmd[T]) QuadKey(quadkey string) *WithinCmd[T] {
	cmd.geom = []any{"QUADKEY", quadkey}
	return cmd
}

// Tile sets the search area to a single XYZ map tile (TILE keyword).
func (cmd *WithinCmd[T]) Tile(x, y, z int) *WithinCmd[T] {
	cmd.geom = []any{"TILE", x, y, z}
	return cmd
}

// IDs selects the IDS output format: WITHIN collection [opts] IDS <area>.
// It is what a fresh command already emits, and is here to switch back.
func (cmd *WithinCmd[T]) IDs() *WithinCmd[string] { return withinAt(cmd, outIDs()) }

// Points selects the POINTS output format: WITHIN collection [opts] POINTS <area>.
func (cmd *WithinCmd[T]) Points() *WithinCmd[NearbyResult] {
	return withinAt(cmd, outPoints("WITHIN"))
}

// Objects selects the OBJECTS output format: WITHIN collection [opts] OBJECTS <area>.
func (cmd *WithinCmd[T]) Objects() *WithinCmd[SearchObject] {
	return withinAt(cmd, outObjects("WITHIN"))
}

// Rects selects the BOUNDS output format: WITHIN collection [opts] BOUNDS <area>.
// Each result is the bounding box of a matching object, lat first.
func (cmd *WithinCmd[T]) Rects() *WithinCmd[RectResult] {
	return withinAt(cmd, outRects("WITHIN"))
}

// Hashes selects the HASHES output format: WITHIN collection [opts] HASHES precision <area>.
// Each result is the geohash of a matching object's centre.
func (cmd *WithinCmd[T]) Hashes(precision int) *WithinCmd[HashResult] {
	return withinAt(cmd, outHashes("WITHIN", precision))
}

// A5Cells selects the A5 output format: WITHIN collection [opts] A5 level <area>.
// Requires a server built from upstream master.
func (cmd *WithinCmd[T]) A5Cells(level int) *WithinCmd[A5Result] {
	return withinAt(cmd, outA5Cells("WITHIN", level))
}

// Do executes the command in whichever output format was selected, defaulting
// to IDS. It reports ErrTruncated when Tile38 capped an unbounded search.
func (cmd *WithinCmd[T]) Do(ctx context.Context) ([]T, error) {
	return searchDo(ctx, cmd.searchState, cmd.out)
}

// Count executes: WITHIN collection [opts] COUNT <area>.
// It is not part of the Do path: COUNT is exempt from Tile38's result cap and
// replies with a bare integer rather than a cursor and a list.
func (cmd *WithinCmd[T]) Count(ctx context.Context) (int, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs([]string{"COUNT"})...)
	if err != nil {
		return 0, fmt.Errorf("tile38: WITHIN COUNT: %w", err)
	}
	return parseCount("WITHIN", val)
}

// Fence opens a live geofence: WITHIN collection [opts] FENCE [DETECT …] <area>.
// The returned Stream holds a dedicated connection and delivers events until it
// is closed or ctx is cancelled.
func (cmd *WithinCmd[T]) Fence(ctx context.Context) (*Stream, error) {
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

// HookCmd builds a Tile38 SETHOOK command: an endpoint, a spatial trigger
// (Nearby/Within), optional Detect/Commands filters, and one fence area
// (Bounds/Circle/Object/Get, or Roam).
//
// Methods may be chained in any order; the parts are assembled into protocol
// order when the command runs.
type HookCmd struct {
	c         *Client
	name      string
	endpoints []string
	meta      [][2]string
	ex        *int
	trigger   []any // NEARBY|WITHIN|INTERSECTS collection
	args      []any // repeatable options that follow the trigger
	detect    []DetectState
	commands  []Command
	nodwell   bool
	distance  bool
	geom      []any // fence area
	radius    *int  // trailing metres of a POINT area
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

// Nearby selects the NEARBY spatial trigger. Use with Point and Radius, or with
// Roam.
func (cmd *HookCmd) Nearby(collection string) *HookCmd {
	cmd.trigger = []any{"NEARBY", collection}
	return cmd
}

// Within selects the WITHIN spatial trigger. Use with any fence area.
func (cmd *HookCmd) Within(collection string) *HookCmd {
	cmd.trigger = []any{"WITHIN", collection}
	return cmd
}

// Intersects selects the INTERSECTS spatial trigger, which fires on any overlap
// with the fence area rather than requiring full containment.
func (cmd *HookCmd) Intersects(collection string) *HookCmd {
	cmd.trigger = []any{"INTERSECTS", collection}
	return cmd
}

// Meta attaches a key/value pair to the hook, echoed back on every event it
// produces. It accumulates: each call adds another pair.
func (cmd *HookCmd) Meta(key, value string) *HookCmd {
	cmd.meta = append(cmd.meta, [2]string{key, value})
	return cmd
}

// EX sets how long the hook lives before Tile38 removes it, in seconds.
func (cmd *HookCmd) EX(secs int) *HookCmd {
	cmd.ex = &secs
	return cmd
}

// Match filters the trigger collection by object ID pattern (glob-style, e.g.
// "org:*"), matching Tile38's MATCH keyword. It accumulates: each call adds
// another pattern.
func (cmd *HookCmd) Match(pattern string) *HookCmd {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// Where sets an optional Tile38 field expression filter.
func (cmd *HookCmd) Where(expr string) *HookCmd {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// Detect restricts the hook to the given transitions. When omitted, Tile38's
// default detect set applies.
func (cmd *HookCmd) Detect(states ...DetectState) *HookCmd {
	cmd.detect = states
	return cmd
}

// Commands restricts the hook to events caused by the given commands.
func (cmd *HookCmd) Commands(commands ...Command) *HookCmd {
	cmd.commands = commands
	return cmd
}

// Roam fires when objects in the trigger collection come within radiusM metres
// of an object in collection. Use with Nearby.
//
// Objects that stay in range keep reporting on each update; chain NoDwell to
// suppress those.
func (cmd *HookCmd) Roam(collection string, radiusM int) *HookCmd {
	cmd.geom = []any{"ROAM", collection, "*", radiusM}
	return cmd
}

// NoDwell stops a roaming fence from re-reporting objects that stay within range
// between updates, matching Tile38's NODWELL keyword. It only affects Roam
// fences, and it is opt-in: dwelling is Tile38's own default.
func (cmd *HookCmd) NoDwell() *HookCmd {
	cmd.nodwell = true
	return cmd
}

// Distance adds each object's distance from the fence centre to every event the
// fence produces, matching Tile38's DISTANCE keyword. It arrives on FenceEvent
// as Distance, and applies to the live fence only — a plain query reads the same
// value through PointsWithDistance.
func (cmd *HookCmd) Distance() *HookCmd {
	cmd.distance = true
	return cmd
}

// Bounds sets the fence area to a lat/lon bounding box. Pass GlobalBounds() to
// fence the whole world.
func (cmd *HookCmd) Bounds(swLat, swLon, neLat, neLon float64) *HookCmd {
	cmd.geom = []any{"BOUNDS", swLat, swLon, neLat, neLon}
	return cmd
}

// Circle sets the fence area to a circle with centre + radius in metres.
func (cmd *HookCmd) Circle(lat, lon float64, radius int) *HookCmd {
	cmd.geom = []any{"CIRCLE", lat, lon, radius}
	return cmd
}

// Point sets the fence area to a point, and is the area a Nearby trigger takes:
// NEARBY reads "POINT lat lon meters" and rejects CIRCLE, so a hook or channel
// fencing on NEARBY needs this rather than Circle. Pair it with Radius.
func (cmd *HookCmd) Point(lat, lon float64) *HookCmd {
	cmd.geom = []any{"POINT", lat, lon}
	return cmd
}

// Radius sets the trailing metres of a Point area. Named for the value it
// carries: Tile38 has no keyword for it, it is the last argument of
// "POINT lat lon meters".
func (cmd *HookCmd) Radius(metres int) *HookCmd {
	cmd.radius = &metres
	return cmd
}

// Object sets the fence area to an inline GeoJSON string.
func (cmd *HookCmd) Object(geojson string) *HookCmd {
	cmd.geom = []any{"OBJECT", geojson}
	return cmd
}

// Sector sets the search area to a circular sector: a circle of radius metres
// centred on lat/lon, clipped to the arc between two compass bearings in
// degrees. Matches Tile38's SECTOR keyword, which NEARBY does not accept.
func (cmd *HookCmd) Sector(lat, lon float64, metres int, bearing1, bearing2 float64) *HookCmd {
	cmd.geom = []any{"SECTOR", lat, lon, metres, bearing1, bearing2}
	return cmd
}

// Hash sets the search area to the box a geohash covers, matching Tile38's HASH
// keyword. The shorter the hash, the larger the box.
func (cmd *HookCmd) Hash(geohash string) *HookCmd {
	cmd.geom = []any{"HASH", geohash}
	return cmd
}

// QuadKey sets the search area to the tile a Bing Maps quadkey names, matching
// Tile38's QUADKEY keyword. Tile is the same area expressed as x/y/z.
func (cmd *HookCmd) QuadKey(quadkey string) *HookCmd {
	cmd.geom = []any{"QUADKEY", quadkey}
	return cmd
}

// Get sets the fence area to an object already stored in Tile38.
func (cmd *HookCmd) Get(collection, id string) *HookCmd {
	cmd.geom = []any{"GET", collection, id}
	return cmd
}

// Do executes the SETHOOK command.
func (cmd *HookCmd) Do(ctx context.Context) error {
	head := hookHead([]any{"SETHOOK", cmd.name, strings.Join(cmd.endpoints, ",")}, cmd.meta, cmd.ex)
	head = append(head, cmd.trigger...)
	args := buildSearch(append(head, cmd.args...), searchOpts{},
		fenceTokens(cmd.distance, cmd.detect, cmd.commands, cmd.nodwell), nil,
		pointGeometry(cmd.geom, cmd.radius))
	if _, err := cmd.c.do(ctx, args...); err != nil {
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
