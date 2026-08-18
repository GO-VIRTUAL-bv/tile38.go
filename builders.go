// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"context"
	"fmt"
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
	if o.nofields {
		out = append(out, "NOFIELDS")
	}
	if o.clip {
		out = append(out, "CLIP")
	}
	return out
}

// whereInTokens renders WHEREIN, which is length-prefixed:
// WHEREIN field <count> value…
func whereInTokens(field string, values []any) []any {
	out := make([]any, 0, len(values)+3)
	out = append(out, "WHEREIN", field, len(values))
	return append(out, values...)
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
// Tile38, so they are stored as values and rendered once here.
func fenceTokens(detect []DetectState, commands []Command, nodwell bool) []any {
	out := make([]any, 0, 6)
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
	c    *Client
	args []any
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
	val, err := cmd.c.do(ctx, append(cmd.args, "POINT")...)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("tile38: GET POINT: %w", err)
	}
	return parseCoords("GET POINT", val)
}

// Object executes: GET collection id — returns the raw GeoJSON string.
func (cmd *GetCmd) Object(ctx context.Context) (string, error) {
	val, err := cmd.c.do(ctx, cmd.args...)
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
	val, err := cmd.c.do(ctx, append(cmd.args, "BOUNDS")...)
	if err != nil {
		return BoundsResult{}, fmt.Errorf("tile38: GET BOUNDS: %w", err)
	}
	return parseBoundsResult("GET", val, false)
}

// Hash executes: GET collection id HASH precision — returns the geohash at the given precision.
func (cmd *GetCmd) Hash(ctx context.Context, precision int) (string, error) {
	val, err := cmd.c.do(ctx, append(cmd.args, "HASH", precision)...)
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
	val, err := cmd.c.do(ctx, append(cmd.args, "A5", level)...)
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
type NearbyCmd struct {
	c         *Client
	args      []any // verb, key, and repeatable options
	opts      searchOpts
	cursorOut uint64 // cursor from the last executed terminal
	detect    []DetectState
	commands  []Command
	nodwell   bool
	geom      []any // POINT lat lon, or ROAM key pattern metres
	radius    *int  // trailing radius, in metres
}

// Limit caps the number of results. Zero means no limit.
func (cmd *NearbyCmd) Limit(n int) *NearbyCmd {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a search from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported. Setting it also means the
// caller is paging deliberately, so a truncated result no longer reports
// ErrTruncated. Tile38 rejects CURSOR on a fence, so Fence ignores it.
func (cmd *NearbyCmd) Cursor(n uint64) *NearbyCmd {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed terminal. It is
// non-zero only when Tile38 stopped at the limit with more objects matching.
func (cmd *NearbyCmd) NextCursor() uint64 { return cmd.cursorOut }

// Where sets an optional Tile38 field expression filter.
func (cmd *NearbyCmd) Where(expr string) *NearbyCmd {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// Match filters results by ID pattern (glob-style, e.g. "truck:*").
func (cmd *NearbyCmd) Match(pattern string) *NearbyCmd {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *NearbyCmd) WhereIn(field string, values ...any) *NearbyCmd {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
func (cmd *NearbyCmd) NoFields() *NearbyCmd {
	cmd.opts.nofields = true
	return cmd
}

// Sparse spreads results evenly over the search area at the given depth (1-8),
// matching Tile38's SPARSE keyword. Tile38 rejects SPARSE combined with Limit.
func (cmd *NearbyCmd) Sparse(depth int) *NearbyCmd {
	cmd.opts.sparse = &depth
	return cmd
}

// Detect restricts a live fence to the given transitions. Only meaningful with Fence.
func (cmd *NearbyCmd) Detect(states ...DetectState) *NearbyCmd {
	cmd.detect = states
	return cmd
}

// Commands restricts a live fence to events caused by the given commands.
// Only meaningful with Fence.
func (cmd *NearbyCmd) Commands(commands ...Command) *NearbyCmd {
	cmd.commands = commands
	return cmd
}

// Point sets the centre coordinates.
func (cmd *NearbyCmd) Point(lat, lon float64) *NearbyCmd {
	cmd.geom = []any{"POINT", lat, lon}
	return cmd
}

// Radius sets the search radius in metres. It applies to a Point area; a Roam
// area carries its own radius.
func (cmd *NearbyCmd) Radius(metres int) *NearbyCmd {
	cmd.radius = &metres
	return cmd
}

// Roam turns the fence into a roaming one: it fires as objects in this
// collection move within radiusM metres of an object in collection. Tile38
// accepts ROAM only on a live NEARBY fence, so this needs Fence — the plain
// query terminators will be rejected by the server.
func (cmd *NearbyCmd) Roam(collection string, radiusM int) *NearbyCmd {
	cmd.geom = []any{"ROAM", collection, "*", radiusM}
	return cmd
}

// NoDwell stops a roaming fence from re-reporting objects that stay within
// range between updates, matching Tile38's NODWELL keyword. It only affects
// Roam fences.
func (cmd *NearbyCmd) NoDwell() *NearbyCmd {
	cmd.nodwell = true
	return cmd
}

// geometry returns the search area with its trailing radius.
func (cmd *NearbyCmd) geometry() []any {
	return pointGeometry(cmd.geom, cmd.radius)
}

func (cmd *NearbyCmd) execArgs(format ...string) []any {
	// No fence clause: Detect and Commands only apply to Fence.
	return buildSearch(cmd.args, cmd.opts, nil, format, cmd.geometry())
}

// IDs executes: NEARBY collection [opts] IDS POINT lat lon radius
func (cmd *NearbyCmd) IDs(ctx context.Context) ([]string, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("IDS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: NEARBY IDs: %w", err)
	}
	res, cursor, err := parseScanIDs(val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// Points executes: NEARBY collection [opts] POINTS POINT lat lon radius
func (cmd *NearbyCmd) Points(ctx context.Context) ([]NearbyResult, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("POINTS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: NEARBY POINTS: %w", err)
	}
	res, cursor, err := parseNearbyPoints(val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// PointsWithDistance executes: NEARBY collection [opts] DISTANCE POINTS POINT lat lon radius
// DISTANCE is an option token, so it has to precede the output format.
func (cmd *NearbyCmd) PointsWithDistance(ctx context.Context) ([]NearbyResultWithDistance, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("DISTANCE", "POINTS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: NEARBY DISTANCE POINTS: %w", err)
	}
	res, cursor, err := parsePointsWithDistance(val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// Count executes: NEARBY collection [opts] COUNT POINT lat lon radius
func (cmd *NearbyCmd) Count(ctx context.Context) (int, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("COUNT")...)
	if err != nil {
		return 0, fmt.Errorf("tile38: NEARBY COUNT: %w", err)
	}
	return parseCount("NEARBY", val)
}

// Objects executes: NEARBY collection [opts] OBJECTS POINT lat lon radius
func (cmd *NearbyCmd) Objects(ctx context.Context) ([]SearchObject, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("OBJECTS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: NEARBY OBJECTS: %w", err)
	}
	res, cursor, err := parseObjects("NEARBY", val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// Fence opens a live geofence: NEARBY collection [opts] FENCE [DETECT …] POINT lat lon radius.
// The returned Stream holds a dedicated connection and delivers events until it
// is closed or ctx is cancelled.
func (cmd *NearbyCmd) Fence(ctx context.Context) (*Stream, error) {
	args := buildSearch(cmd.args, cmd.opts.fenceOpts(),
		fenceTokens(cmd.detect, cmd.commands, cmd.nodwell), nil, cmd.geometry())
	return cmd.c.fenceStream(ctx, args)
}

// ScanCmd builds a Tile38 SCAN command.
type ScanCmd struct {
	c         *Client
	args      []any // verb, key, and repeatable options
	opts      searchOpts
	cursorOut uint64 // cursor from the last executed terminal
}

// Limit caps the number of results. Zero means no limit.
func (cmd *ScanCmd) Limit(n int) *ScanCmd {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a search from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported. Setting it also means the
// caller is paging deliberately, so a truncated result no longer reports
// ErrTruncated. Tile38 rejects CURSOR on a fence, so Fence ignores it.
func (cmd *ScanCmd) Cursor(n uint64) *ScanCmd {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed terminal. It is
// non-zero only when Tile38 stopped at the limit with more objects matching.
func (cmd *ScanCmd) NextCursor() uint64 { return cmd.cursorOut }

// Match filters results by ID pattern (glob-style, e.g. "truck:*").
func (cmd *ScanCmd) Match(pattern string) *ScanCmd {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// Where sets an optional Tile38 field expression filter.
func (cmd *ScanCmd) Where(expr string) *ScanCmd {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *ScanCmd) WhereIn(field string, values ...any) *ScanCmd {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
// SCAN takes no SPARSE — Tile38 rejects it for this command.
func (cmd *ScanCmd) NoFields() *ScanCmd {
	cmd.opts.nofields = true
	return cmd
}

// SCAN takes no fence clause and no geometry, but it still has to render LIMIT
// through buildSearch — appending the output format directly would drop it.
func (cmd *ScanCmd) execArgs(format ...string) []any {
	return buildSearch(cmd.args, cmd.opts, nil, format, nil)
}

// IDs executes: SCAN collection [opts] IDS
func (cmd *ScanCmd) IDs(ctx context.Context) ([]string, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("IDS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: SCAN IDs: %w", err)
	}
	res, cursor, err := parseScanIDs(val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// Points executes: SCAN collection [opts] POINTS
func (cmd *ScanCmd) Points(ctx context.Context) ([]NearbyResult, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("POINTS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: SCAN POINTS: %w", err)
	}
	res, cursor, err := parseScanPoints(val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// Count executes: SCAN collection [opts] COUNT
func (cmd *ScanCmd) Count(ctx context.Context) (int, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("COUNT")...)
	if err != nil {
		return 0, fmt.Errorf("tile38: SCAN COUNT: %w", err)
	}
	return parseCount("SCAN", val)
}

// Objects executes: SCAN collection [opts] OBJECTS
func (cmd *ScanCmd) Objects(ctx context.Context) ([]SearchObject, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("OBJECTS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: SCAN OBJECTS: %w", err)
	}
	res, cursor, err := parseObjects("SCAN", val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// WithinCmd builds a Tile38 WITHIN query. Methods may be chained in any order;
// the parts are assembled into protocol order when the command runs.
type WithinCmd struct {
	c         *Client
	args      []any // verb, key, and repeatable options
	opts      searchOpts
	cursorOut uint64 // cursor from the last executed terminal
	detect    []DetectState
	commands  []Command
	geom      []any // search area
}

// Limit caps the number of results. Zero means no limit.
func (cmd *WithinCmd) Limit(n int) *WithinCmd {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a search from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported. Setting it also means the
// caller is paging deliberately, so a truncated result no longer reports
// ErrTruncated. Tile38 rejects CURSOR on a fence, so Fence ignores it.
func (cmd *WithinCmd) Cursor(n uint64) *WithinCmd {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed terminal. It is
// non-zero only when Tile38 stopped at the limit with more objects matching.
func (cmd *WithinCmd) NextCursor() uint64 { return cmd.cursorOut }

// Where sets an optional Tile38 field expression filter.
func (cmd *WithinCmd) Where(expr string) *WithinCmd {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// Match filters results by ID pattern (glob-style, e.g. "truck:*").
func (cmd *WithinCmd) Match(pattern string) *WithinCmd {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *WithinCmd) WhereIn(field string, values ...any) *WithinCmd {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
func (cmd *WithinCmd) NoFields() *WithinCmd {
	cmd.opts.nofields = true
	return cmd
}

// Clip trims returned objects to the search area rather than returning them
// whole, matching Tile38's CLIP keyword.
func (cmd *WithinCmd) Clip() *WithinCmd {
	cmd.opts.clip = true
	return cmd
}

// Sparse spreads results evenly over the search area at the given depth (1-8),
// matching Tile38's SPARSE keyword. Tile38 rejects SPARSE combined with Limit.
func (cmd *WithinCmd) Sparse(depth int) *WithinCmd {
	cmd.opts.sparse = &depth
	return cmd
}

// Detect restricts a live fence to the given transitions. Only meaningful with Fence.
func (cmd *WithinCmd) Detect(states ...DetectState) *WithinCmd {
	cmd.detect = states
	return cmd
}

// Commands restricts a live fence to events caused by the given commands.
// Only meaningful with Fence.
func (cmd *WithinCmd) Commands(commands ...Command) *WithinCmd {
	cmd.commands = commands
	return cmd
}

// Get sets the search area to an object already stored in Tile38 (GET keyword).
func (cmd *WithinCmd) Get(collection, id string) *WithinCmd {
	cmd.geom = []any{"GET", collection, id}
	return cmd
}

// Object sets the search area to an inline GeoJSON string (OBJECT keyword).
func (cmd *WithinCmd) Object(geojson string) *WithinCmd {
	cmd.geom = []any{"OBJECT", geojson}
	return cmd
}

// Bounds sets the search area to a lat/lon bounding box (BOUNDS keyword).
func (cmd *WithinCmd) Bounds(swLat, swLon, neLat, neLon float64) *WithinCmd {
	cmd.geom = []any{"BOUNDS", swLat, swLon, neLat, neLon}
	return cmd
}

// Circle sets the search area to a circle with centre + radius in metres (CIRCLE keyword).
func (cmd *WithinCmd) Circle(lat, lon float64, radius int) *WithinCmd {
	cmd.geom = []any{"CIRCLE", lat, lon, radius}
	return cmd
}

// A5 sets the search area to a single A5 cell's pentagon, identified by its hex
// cell id (A5 keyword). Requires a server built from upstream master: A5 is
// merged upstream but has shipped in no release tag as of 1.38.0. Tile38 accepts
// A5 as a search area only, not as a hook or channel fence area.
func (cmd *WithinCmd) A5(cellID string) *WithinCmd {
	cmd.geom = []any{"A5", cellID}
	return cmd
}

// Tile sets the search area to a single XYZ map tile (TILE keyword).
func (cmd *WithinCmd) Tile(x, y, z int) *WithinCmd {
	cmd.geom = []any{"TILE", x, y, z}
	return cmd
}

func (cmd *WithinCmd) execArgs(format ...string) []any {
	// No fence clause: Detect and Commands only apply to Fence.
	return buildSearch(cmd.args, cmd.opts, nil, format, cmd.geom)
}

// IDs executes: WITHIN collection [opts] IDS area
func (cmd *WithinCmd) IDs(ctx context.Context) ([]string, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("IDS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: WITHIN IDs: %w", err)
	}
	res, cursor, err := parseScanIDs(val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// Points executes: WITHIN collection [opts] POINTS area
func (cmd *WithinCmd) Points(ctx context.Context) ([]NearbyResult, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("POINTS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: WITHIN POINTS: %w", err)
	}
	res, cursor, err := parseNearbyPoints(val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// Count executes: WITHIN collection [opts] COUNT area
func (cmd *WithinCmd) Count(ctx context.Context) (int, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("COUNT")...)
	if err != nil {
		return 0, fmt.Errorf("tile38: WITHIN COUNT: %w", err)
	}
	return parseCount("WITHIN", val)
}

// Objects executes: WITHIN collection [opts] OBJECTS area
func (cmd *WithinCmd) Objects(ctx context.Context) ([]SearchObject, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("OBJECTS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: WITHIN OBJECTS: %w", err)
	}
	res, cursor, err := parseObjects("WITHIN", val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// Fence opens a live geofence: WITHIN collection [opts] FENCE [DETECT …] area.
// The returned Stream holds a dedicated connection and delivers events until it
// is closed or ctx is cancelled.
func (cmd *WithinCmd) Fence(ctx context.Context) (*Stream, error) {
	args := buildSearch(cmd.args, cmd.opts.fenceOpts(),
		fenceTokens(cmd.detect, cmd.commands, false), nil, cmd.geom)
	return cmd.c.fenceStream(ctx, args)
}

// ── Hooks ─────────────────────────────────────────────────────────────────────

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
		fenceTokens(cmd.detect, cmd.commands, cmd.nodwell), nil,
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

// Point sets the POINT coordinates.
func (cmd *PipelineSetCmd) Point(lat, lon float64) *PipelineSetCmd {
	cmd.args = append(cmd.args, "POINT", lat, lon)
	return cmd
}

// Queue enqueues the command onto the pipeline. Context is supplied at Flush.
func (cmd *PipelineSetCmd) Queue() {
	cmd.p.cmds = append(cmd.p.cmds, cmd.args)
}
