// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build integration

// Integration tests run against a real Tile38 in Docker:
//
//	go test -tags=integration ./...
//
// Override the image with TILE38_IMAGE.
package tile38

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// addr points at the shared Tile38 container. Every test uses its own
// collection key so they stay independent.
var addr string

func TestMain(m *testing.M) {
	// The supported server is pinned in .version, shared with the
	// workflow that watches upstream for a newer one. It is an edge digest
	// rather than a release tag because A5 is merged upstream but ships in no
	// tag yet; the digest keeps a push to upstream master from silently
	// changing what CI tests against.
	image := os.Getenv("TILE38_IMAGE")
	if image == "" {
		b, err := os.ReadFile(".version")
		if err != nil {
			fmt.Fprintln(os.Stderr, "read .version:", err)
			os.Exit(1)
		}
		image = strings.TrimSpace(string(b))
	}

	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        image,
			ExposedPorts: []string{"9851/tcp"},
			WaitingFor:   wait.ForListeningPort("9851/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "start tile38:", err)
		os.Exit(1)
	}

	endpoint, err := container.PortEndpoint(ctx, "9851/tcp", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "endpoint:", err)
		os.Exit(1)
	}
	addr = endpoint

	if err := waitReady(ctx, addr); err != nil {
		fmt.Fprintln(os.Stderr, "tile38 not ready:", err)
		_ = testcontainers.TerminateContainer(container)
		os.Exit(1)
	}

	code := m.Run()
	_ = testcontainers.TerminateContainer(container)
	os.Exit(code)
}

// waitReady blocks until Tile38 answers a command. ForListeningPort returns as
// soon as the port accepts a connection, which can happen before the server will
// serve one — the first connection then comes back "connection reset by peer",
// and whichever test ran first absorbed the failure.
func waitReady(ctx context.Context, addr string) error {
	c := New(addr)
	defer c.Close()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		err := c.Ping(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// newClient returns a Client and the collection key reserved for this test.
func newClient(t *testing.T) (*Client, string) {
	t.Helper()
	c := New(addr)
	t.Cleanup(func() { _ = c.Close() })
	key := t.Name()
	t.Cleanup(func() {
		// DROP on a missing collection is not an error worth failing on.
		_ = c.Drop(key).Do(context.Background())
	})
	if err := c.Ping(t.Context()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return c, key
}

func TestCRUD(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	if err := c.Set(key, "truck1").Field("speed", 42).Point(33.5, -115.5).Do(ctx); err != nil {
		t.Fatalf("set: %v", err)
	}

	lat, lon, err := c.Get(key, "truck1").Point(ctx)
	if err != nil {
		t.Fatalf("get point: %v", err)
	}
	if lat != 33.5 || lon != -115.5 {
		t.Errorf("point = %v, %v; want 33.5, -115.5", lat, lon)
	}

	geojson, err := c.Get(key, "truck1").Object(ctx)
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	if geojson == "" {
		t.Error("get object returned empty geojson")
	}

	// WITHFIELDS wraps the reply, so geometry and fields arrive in one round trip
	// rather than an FGet per field.
	withFields := c.Get(key, "truck1").WithFields()
	if geojson, err = withFields.Object(ctx); err != nil {
		t.Fatalf("get withfields: %v", err)
	}
	if geojson == "" || withFields.Fields()["speed"] != "42" {
		t.Errorf("get withfields = %q, fields %v; want speed=42", geojson, withFields.Fields())
	}
	// An object with no non-zero fields still decodes: Tile38 omits the fields
	// element from the envelope entirely.
	if err := c.Set(key, "bare").Point(1, 1).Do(ctx); err != nil {
		t.Fatalf("set bare: %v", err)
	}
	bare := c.Get(key, "bare").WithFields()
	if _, _, err := bare.Point(ctx); err != nil {
		t.Fatalf("get bare withfields: %v", err)
	}
	if bare.Fields() != nil {
		t.Errorf("bare fields = %v, want nil", bare.Fields())
	}

	speed, err := c.FGet(key, "truck1", "speed").Do(ctx)
	if err != nil {
		t.Fatalf("fget: %v", err)
	}
	if speed != "42" {
		t.Errorf("speed = %q, want %q", speed, "42")
	}

	if err := c.FSet(key, "truck1").Field("speed", 55).Do(ctx); err != nil {
		t.Fatalf("fset: %v", err)
	}
	if speed, _ = c.FGet(key, "truck1", "speed").Do(ctx); speed != "55" {
		t.Errorf("speed after fset = %q, want %q", speed, "55")
	}

	ok, err := c.Exists(key, "truck1").Do(ctx)
	if err != nil || !ok {
		t.Errorf("exists = %v, %v; want true, nil", ok, err)
	}

	if err := c.Del(key, "truck1").Do(ctx); err != nil {
		t.Fatalf("del: %v", err)
	}
	if ok, _ := c.Exists(key, "truck1").Do(ctx); ok {
		t.Error("exists after del = true, want false")
	}
}

func TestTTL(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	if err := c.Set(key, "t").Point(1, 1).Do(ctx); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := c.TTL(key, "t").Do(ctx); err != nil {
		t.Fatalf("ttl on non-expiring object: %v", err)
	}

	if err := c.Expire(key, "t", 600).Do(ctx); err != nil {
		t.Fatalf("expire: %v", err)
	}
	ttl, err := c.TTL(key, "t").Do(ctx)
	if err != nil {
		t.Fatalf("ttl: %v", err)
	}
	if ttl <= 0 || ttl > 10*time.Minute {
		t.Errorf("ttl = %v, want (0, 10m]", ttl)
	}

	if err := c.Persist(key, "t").Do(ctx); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if ttl, _ = c.TTL(key, "t").Do(ctx); ttl != -1 {
		t.Errorf("ttl after persist = %v, want -1", ttl)
	}

	// SET with an inline TTL takes the same path.
	if err := c.Set(key, "t2").EX(300).Point(1, 1).Do(ctx); err != nil {
		t.Fatalf("set with ttl: %v", err)
	}
	if ttl, _ = c.TTL(key, "t2").Do(ctx); ttl <= 0 {
		t.Errorf("inline ttl = %v, want > 0", ttl)
	}
}

func TestSearchOutputFormats(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	for _, o := range []struct {
		id       string
		lat, lon float64
	}{
		{"a", 33.500, -115.500},
		{"b", 33.501, -115.501},
		{"far", 40.000, -100.000},
	} {
		if err := c.Set(key, o.id).Field("speed", 10).Point(o.lat, o.lon).Do(ctx); err != nil {
			t.Fatalf("set %s: %v", o.id, err)
		}
	}

	ids, err := c.Nearby(key).Point(33.5, -115.5).Radius(5000).IDs(ctx)
	if err != nil {
		t.Fatalf("nearby ids: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("nearby ids = %v, want 2 results", ids)
	}

	pts, err := c.Nearby(key).Point(33.5, -115.5).Radius(5000).Points(ctx)
	if err != nil {
		t.Fatalf("nearby points: %v", err)
	}
	if len(pts) != 2 || pts[0].Lat == 0 || pts[0].Lon == 0 {
		t.Errorf("nearby points = %+v", pts)
	}

	// DISTANCE is an option token, and the distance arrives after the optional
	// fields array — both easy to get wrong.
	withDist, err := c.Nearby(key).Point(33.5, -115.5).Radius(5000).PointsWithDistance(ctx)
	if err != nil {
		t.Fatalf("nearby points with distance: %v", err)
	}
	if len(withDist) != 2 {
		t.Fatalf("points with distance = %+v, want 2 results", withDist)
	}
	if withDist[0].Distance != 0 {
		t.Errorf("nearest distance = %v, want 0 (query centre)", withDist[0].Distance)
	}
	if withDist[1].Distance <= 0 {
		t.Errorf("second distance = %v, want > 0", withDist[1].Distance)
	}
	if withDist[1].Lat == 0 || withDist[1].Lon == 0 {
		t.Errorf("coords lost when parsing distance: %+v", withDist[1])
	}

	// COUNT replies with a bare integer, not a [count, …] array.
	n, err := c.Nearby(key).Point(33.5, -115.5).Radius(5000).Count(ctx)
	if err != nil {
		t.Fatalf("nearby count: %v", err)
	}
	if n != 2 {
		t.Errorf("nearby count = %d, want 2", n)
	}

	objs, err := c.Nearby(key).Point(33.5, -115.5).Radius(5000).Objects(ctx)
	if err != nil {
		t.Fatalf("nearby objects: %v", err)
	}
	if len(objs) != 2 || objs[0].GeoJSON == "" {
		t.Errorf("nearby objects = %+v", objs)
	}
	// Every object above was SET with speed=10, so a real server must hand the
	// field back beside the geometry rather than needing an FGet per object.
	if objs[0].Fields["speed"] != "10" {
		t.Errorf("nearby object fields = %v, want speed=10", objs[0].Fields)
	}
	// POINTS carries the same fields array — on its own at the end, and before
	// the distance when DISTANCE is asked for.
	if pts[0].Fields["speed"] != "10" {
		t.Errorf("nearby points fields = %v, want speed=10", pts[0].Fields)
	}
	if withDist[0].Fields["speed"] != "10" {
		t.Errorf("nearby points-with-distance fields = %v, want speed=10", withDist[0].Fields)
	}

	if n, err = c.Nearby(key).Limit(1).Point(33.5, -115.5).Radius(5000).Count(ctx); err != nil || n != 1 {
		t.Errorf("nearby limit 1 count = %d, %v; want 1, nil", n, err)
	}
	if ids, err = c.Nearby(key).Where("speed > 5").Point(33.5, -115.5).Radius(5000).IDs(ctx); err != nil || len(ids) != 2 {
		t.Errorf("nearby where = %v, %v; want 2 results", ids, err)
	}
	if ids, err = c.Nearby(key).Where("speed > 500").Point(33.5, -115.5).Radius(5000).IDs(ctx); err != nil || len(ids) != 0 {
		t.Errorf("nearby where (no match) = %v, %v; want 0 results", ids, err)
	}
}

func TestWithinAndIntersects(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	if err := c.Set(key, "a").Point(33.5, -115.5).Do(ctx); err != nil {
		t.Fatalf("set: %v", err)
	}

	for name, got := range map[string]func() (int, error){
		"within bounds":     func() (int, error) { return c.Within(key).Bounds(33, -116, 34, -115).Count(ctx) },
		"within circle":     func() (int, error) { return c.Within(key).Circle(33.5, -115.5, 5000).Count(ctx) },
		"intersects bounds": func() (int, error) { return c.Intersects(key).Bounds(33, -116, 34, -115).Count(ctx) },
		"intersects circle": func() (int, error) { return c.Intersects(key).Circle(33.5, -115.5, 5000).Count(ctx) },
	} {
		n, err := got()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if n != 1 {
			t.Errorf("%s count = %d, want 1", name, n)
		}
	}

	objs, err := c.Within(key).Object(`{"type":"Polygon","coordinates":[[[-116,33],[-115,33],[-115,34],[-116,34],[-116,33]]]}`).Objects(ctx)
	if err != nil {
		t.Fatalf("within object: %v", err)
	}
	if len(objs) != 1 {
		t.Errorf("within object = %+v, want 1 result", objs)
	}

	if err := c.Set(key, "zone").Bounds(33, -116, 34, -115).Do(ctx); err != nil {
		t.Fatalf("set zone: %v", err)
	}
	ids, err := c.Within(key).Get(key, "zone").IDs(ctx)
	if err != nil {
		t.Fatalf("within get: %v", err)
	}
	if len(ids) == 0 {
		t.Error("within get returned no results")
	}
}

func TestScan(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	for _, id := range []string{"truck:1", "truck:2", "car:1"} {
		if err := c.Set(key, id).Point(1, 1).Do(ctx); err != nil {
			t.Fatalf("set %s: %v", id, err)
		}
	}

	ids, err := c.Scan(key).IDs(ctx)
	if err != nil {
		t.Fatalf("scan ids: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("scan ids = %v, want 3", ids)
	}

	if ids, err = c.Scan(key).Match("truck:*").IDs(ctx); err != nil || len(ids) != 2 {
		t.Errorf("scan match = %v, %v; want 2 results", ids, err)
	}

	n, err := c.Scan(key).Count(ctx)
	if err != nil || n != 3 {
		t.Errorf("scan count = %d, %v; want 3, nil", n, err)
	}

	pts, err := c.Scan(key).Points(ctx)
	if err != nil || len(pts) != 3 {
		t.Errorf("scan points = %+v, %v; want 3 results", pts, err)
	}
}

// Object bounds come back lat-first while collection bounds come back
// lon-first; both must land in BoundsResult as {lat, lon}.
// A point can carry a third ordinate, which Tile38 stores and echoes back but
// omits from the reply whenever it is zero — so a two-element and a
// three-element coordinate array turn up in the same response.
func TestPointZ(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	if err := c.Set(key, "flying").Object(
		`{"type":"Point","coordinates":[-115.5,33.5,120.5]}`).Do(ctx); err != nil {
		t.Fatalf("set 3d: %v", err)
	}
	if err := c.Set(key, "ground").Point(33.6, -115.6).Do(ctx); err != nil {
		t.Fatalf("set 2d: %v", err)
	}

	lat, lon, z, err := c.Get(key, "flying").PointZ(ctx)
	if err != nil {
		t.Fatalf("get point z: %v", err)
	}
	if lat != 33.5 || lon != -115.5 || z != 120.5 {
		t.Errorf("get point z = %v,%v,%v; want 33.5,-115.5,120.5", lat, lon, z)
	}
	if _, _, z, err = c.Get(key, "ground").PointZ(ctx); err != nil || z != 0 {
		t.Errorf("get 2d point z = %v, %v; want 0", z, err)
	}

	pts, err := c.Scan(key).Points(ctx)
	if err != nil {
		t.Fatalf("scan points: %v", err)
	}
	byID := map[string]NearbyResult{}
	for _, p := range pts {
		byID[p.ID] = p
	}
	if byID["flying"].Z != 120.5 {
		t.Errorf("scan points z = %v, want 120.5", byID["flying"].Z)
	}
	if byID["ground"].Z != 0 {
		t.Errorf("scan points 2d z = %v, want 0", byID["ground"].Z)
	}

	// The z lives inside the coordinate array, so the trailing distance must not
	// be read as one or vice versa.
	withDist, err := c.Nearby(key).Point(33.5, -115.5).Radius(100000).PointsWithDistance(ctx)
	if err != nil {
		t.Fatalf("points with distance: %v", err)
	}
	if len(withDist) != 2 || withDist[0].ID != "flying" {
		t.Fatalf("points with distance = %+v", withDist)
	}
	if withDist[0].Z != 120.5 || withDist[0].Distance != 0 {
		t.Errorf("nearest = %+v, want z 120.5 and distance 0", withDist[0])
	}
	if withDist[1].Z != 0 || withDist[1].Distance <= 0 {
		t.Errorf("second = %+v, want z 0 and a positive distance", withDist[1])
	}
}

func TestBoundsCoordinateOrder(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	if err := c.Set(key, "box").Bounds(10, 20, 30, 40).Do(ctx); err != nil {
		t.Fatalf("set bounds: %v", err)
	}

	objBounds, err := c.Get(key, "box").Bounds(ctx)
	if err != nil {
		t.Fatalf("get bounds: %v", err)
	}
	want := BoundsResult{SW: [2]float64{10, 20}, NE: [2]float64{30, 40}}
	if objBounds != want {
		t.Errorf("object bounds = %+v, want %+v", objBounds, want)
	}

	collBounds, err := c.Bounds(key).Do(ctx)
	if err != nil {
		t.Fatalf("collection bounds: %v", err)
	}
	if collBounds != want {
		t.Errorf("collection bounds = %+v, want %+v", collBounds, want)
	}
}

// A5 and TILE areas: take the cell the object sits in, then query by that cell.
func TestA5AndTileAreas(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	if err := c.Set(key, "p1").Point(33.5, -115.5).Do(ctx); err != nil {
		t.Fatalf("set: %v", err)
	}

	cell, err := c.Get(key, "p1").A5(ctx, 8)
	if err != nil {
		t.Fatalf("get a5: %v", err)
	}
	if cell == "" {
		t.Fatal("get a5 returned an empty cell id")
	}

	ids, err := c.Within(key).A5(cell).IDs(ctx)
	if err != nil {
		t.Fatalf("within a5: %v", err)
	}
	if len(ids) != 1 || ids[0] != "p1" {
		t.Errorf("within a5 = %v, want [p1]", ids)
	}

	if ids, err = c.Intersects(key).A5(cell).IDs(ctx); err != nil || len(ids) != 1 {
		t.Errorf("intersects a5 = %v, %v; want [p1]", ids, err)
	}

	// Tile 0/0/0 covers the whole world.
	if ids, err = c.Within(key).Tile(0, 0, 0).IDs(ctx); err != nil || len(ids) != 1 {
		t.Errorf("within tile 0/0/0 = %v, %v; want [p1]", ids, err)
	}
}

func TestKeysAndDBSize(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	if err := c.Set(key, "a").Point(1, 1).Do(ctx); err != nil {
		t.Fatalf("set: %v", err)
	}

	keys, err := c.Keys("*").Do(ctx)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	found := false
	for _, k := range keys {
		if k == key {
			found = true
		}
	}
	if !found {
		t.Errorf("keys = %v, want it to contain %q", keys, key)
	}

	// DBSize reads num_objects out of SERVER; Tile38 has no DBSIZE command.
	size, err := c.DBSize().Do(ctx)
	if err != nil {
		t.Fatalf("dbsize: %v", err)
	}
	if size < 1 {
		t.Errorf("dbsize = %d, want >= 1", size)
	}
}

func TestRenamePDelDrop(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	for _, id := range []string{"truck:1", "truck:2", "car:1"} {
		if err := c.Set(key, id).Point(1, 1).Do(ctx); err != nil {
			t.Fatalf("set %s: %v", id, err)
		}
	}

	if err := c.PDel(key, "truck:*").Do(ctx); err != nil {
		t.Fatalf("pdel: %v", err)
	}
	ids, _ := c.Scan(key).IDs(ctx)
	if len(ids) != 1 {
		t.Errorf("ids after pdel = %v, want 1", ids)
	}

	renamed := key + "-renamed"
	t.Cleanup(func() { _ = c.Drop(renamed).Do(context.Background()) })
	if err := c.Rename(key, renamed).Do(ctx); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if ids, _ = c.Scan(renamed).IDs(ctx); len(ids) != 1 {
		t.Errorf("ids after rename = %v, want 1", ids)
	}

	if err := c.Drop(renamed).Do(ctx); err != nil {
		t.Fatalf("drop: %v", err)
	}
}

func TestJSONCommands(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	if err := c.JSet(key, "obj", "name", "truck").Do(ctx); err != nil {
		t.Fatalf("jset: %v", err)
	}
	got, err := c.JGet(key, "obj", "name").Do(ctx)
	if err != nil {
		t.Fatalf("jget: %v", err)
	}
	if got != "truck" {
		t.Errorf("jget = %q, want %q", got, "truck")
	}
	if err := c.JDel(key, "obj", "name").Do(ctx); err != nil {
		t.Fatalf("jdel: %v", err)
	}
}

func TestPipeline(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	p := c.Pipeline()
	const n = 250
	for i := range n {
		p.Set(key, fmt.Sprintf("bulk%d", i)).
			EX(300).
			Field("i", i).
			Point(33.0+float64(i)/10000, -115.0).
			Queue()
	}
	if p.Len() != n {
		t.Fatalf("Len = %d, want %d", p.Len(), n)
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if p.Len() != 0 {
		t.Errorf("Len after flush = %d, want 0", p.Len())
	}

	count, err := c.Scan(key).Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != n {
		t.Errorf("count = %d, want %d", count, n)
	}

	// The connection goes back to the pool after a batch. (The mid-batch
	// server-error path is covered by TestPipelineFlush against a scripted
	// reply: Tile38 accepts every coordinate the typed builder can produce,
	// including out-of-range values and NaN, so it cannot be provoked here.)
	if err := c.Ping(ctx); err != nil {
		t.Errorf("ping after flush: %v", err)
	}
	if err := c.Set(key, "after").Point(1, 1).Do(ctx); err != nil {
		t.Errorf("set after flush: %v", err)
	}
	if err := c.Ping(ctx); err != nil {
		t.Errorf("ping after pipeline error: %v", err)
	}
}

// Tile38 caps a search at 100 results when no LIMIT is given, and reports it by
// returning a non-zero cursor. This is the case a client silently gets wrong.
func TestSearchTruncationAndPaging(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	const n = 250
	p := c.Pipeline()
	for i := range n {
		p.Set(key, fmt.Sprintf("obj%d", i)).Point(33.0+float64(i)/10000, -115.0).Queue()
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// A plain scan stops at the server's default of 100 and says so.
	first := c.Scan(key)
	ids, err := first.IDs(ctx)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("IDs = %v, want ErrTruncated", err)
	}
	if len(ids) != 100 {
		t.Errorf("got %d ids, want the server default of 100", len(ids))
	}
	if first.NextCursor() == 0 {
		t.Error("NextCursor = 0, want a resume point")
	}

	// Paging with that cursor reaches every object exactly once.
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	for cursor := first.NextCursor(); ; {
		page := c.Scan(key).Cursor(cursor)
		pageIDs, err := page.IDs(ctx)
		if err != nil {
			t.Fatalf("page at cursor %d: %v", cursor, err)
		}
		for _, id := range pageIDs {
			if seen[id] {
				t.Errorf("id %q returned twice", id)
			}
			seen[id] = true
		}
		if cursor = page.NextCursor(); cursor == 0 {
			break
		}
	}
	if len(seen) != n {
		t.Errorf("paged over %d ids, want %d", len(seen), n)
	}

	// An explicit limit is the caller's own bound, so it is not an error.
	if _, err := c.Scan(key).Limit(10).IDs(ctx); err != nil {
		t.Errorf("Limit(10): %v", err)
	}
	// A limit above the collection size runs to completion.
	all, err := c.Scan(key).Limit(n * 2).IDs(ctx)
	if err != nil {
		t.Errorf("Limit(%d): %v", n*2, err)
	}
	if len(all) != n {
		t.Errorf("got %d ids, want %d", len(all), n)
	}
}

// The headline capability: a live geofence streamed over the connection, which
// a Redis client cannot do.
func TestLiveFence(t *testing.T) {
	c, key := newClient(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	st, err := c.Nearby(key).Point(33.5, -115.5).Radius(1000).Detect(Enter, Exit).Fence(ctx)
	if err != nil {
		t.Fatalf("fence: %v", err)
	}
	defer st.Close()

	events := make(chan *FenceEvent, 4)
	errs := make(chan error, 1)
	go func() {
		for {
			ev, err := st.Next()
			if err != nil {
				errs <- err
				return
			}
			events <- ev
		}
	}()

	if err := c.Set(key, "rover").Field("speed", 42).Field("driver", "bob").
		Point(33.5, -115.5).Do(ctx); err != nil {
		t.Fatalf("move in: %v", err)
	}
	enter := waitEvent(t, events, errs)
	if enter.Detect != "enter" {
		t.Errorf("first event detect = %q, want %q", enter.Detect, "enter")
	}
	if enter.ID != "rover" || enter.Key != key {
		t.Errorf("first event = %+v", enter)
	}
	if len(enter.Object) == 0 {
		t.Error("first event carried no object geojson")
	}
	// The notification carries the object's fields as JSON, so a consumer never
	// has to go back to the server for the state that triggered the event.
	if enter.Fields["speed"] != "42" || enter.Fields["driver"] != "bob" {
		t.Errorf("first event fields = %v, want speed=42 driver=bob", enter.Fields)
	}

	if err := c.Set(key, "rover").Point(40, -100).Do(ctx); err != nil {
		t.Fatalf("move out: %v", err)
	}
	if exit := waitEvent(t, events, errs); exit.Detect != "exit" {
		t.Errorf("second event detect = %q, want %q", exit.Detect, "exit")
	}

	// Close must unblock the reader.
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, io.EOF) {
			t.Errorf("Next after Close = %v, want io.EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Next still blocked 5s after Close")
	}
}

func TestLiveFenceCommandsFilter(t *testing.T) {
	c, key := newClient(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	st, err := c.Within(key).Bounds(-90, -180, 90, 180).
		Detect(Inside).Commands(CommandSet).Fence(ctx)
	if err != nil {
		t.Fatalf("fence: %v", err)
	}
	defer st.Close()

	events := make(chan *FenceEvent, 4)
	errs := make(chan error, 1)
	go func() {
		for {
			ev, err := st.Next()
			if err != nil {
				errs <- err
				return
			}
			events <- ev
		}
	}()

	if err := c.Set(key, "obj").Point(10, 10).Do(ctx); err != nil {
		t.Fatalf("set: %v", err)
	}
	ev := waitEvent(t, events, errs)
	if ev.Command != "set" {
		t.Errorf("event command = %q, want %q", ev.Command, "set")
	}
	if ev.Detect != "inside" {
		t.Errorf("event detect = %q, want %q", ev.Detect, "inside")
	}
}

func TestSetChanAndSubscribe(t *testing.T) {
	c, key := newClient(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	channel := key + "-chan"
	t.Cleanup(func() { _ = c.DelChan(channel).Do(context.Background()) })

	if err := c.SetChan(channel).Within(key).Detect(Inside).Commands(CommandSet).Bounds(GlobalBounds()).Do(ctx); err != nil {
		t.Fatalf("setchan: %v", err)
	}

	chans, err := c.Chans("*").Do(ctx)
	if err != nil {
		t.Fatalf("chans: %v", err)
	}
	var found *HookInfo
	for i := range chans {
		if chans[i].Name == channel {
			found = &chans[i]
		}
	}
	if found == nil {
		t.Fatalf("chans = %+v, want it to contain %q", chans, channel)
	}
	if found.Key != key {
		t.Errorf("chan key = %q, want %q", found.Key, key)
	}
	if len(found.Endpoints) == 0 || len(found.Command) == 0 {
		t.Errorf("chan descriptor = %+v, want endpoints and command", *found)
	}

	sub, err := c.Subscribe(ctx, channel)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	events := make(chan *FenceEvent, 4)
	errs := make(chan error, 1)
	go func() {
		for {
			ev, err := sub.Next()
			if err != nil {
				errs <- err
				return
			}
			events <- ev
		}
	}()

	if err := c.Set(key, "rover").Point(33.5, -115.5).Do(ctx); err != nil {
		t.Fatalf("set: %v", err)
	}
	ev := waitEvent(t, events, errs)
	if ev.ID != "rover" || ev.Hook != channel {
		t.Errorf("event = %+v, want id rover on hook %q", ev, channel)
	}
}

// PSubscribe must skip the per-pattern acks before the first real message.
func TestPSubscribe(t *testing.T) {
	c, key := newClient(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	channel := key + "-chan"
	t.Cleanup(func() { _ = c.DelChan(channel).Do(context.Background()) })
	if err := c.SetChan(channel).Within(key).Detect(Inside).Commands(CommandSet).Bounds(GlobalBounds()).Do(ctx); err != nil {
		t.Fatalf("setchan: %v", err)
	}

	sub, err := c.PSubscribe(ctx, key+"-*", "nothing-*")
	if err != nil {
		t.Fatalf("psubscribe: %v", err)
	}
	defer sub.Close()

	events := make(chan *FenceEvent, 4)
	errs := make(chan error, 1)
	go func() {
		for {
			ev, err := sub.Next()
			if err != nil {
				errs <- err
				return
			}
			events <- ev
		}
	}()

	if err := c.Set(key, "rover").Point(33.5, -115.5).Do(ctx); err != nil {
		t.Fatalf("set: %v", err)
	}
	if ev := waitEvent(t, events, errs); ev.ID != "rover" {
		t.Errorf("event = %+v, want id rover", ev)
	}
}

func TestHooks(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	hook := key + "-hook"
	t.Cleanup(func() { _ = c.DelHook(hook).Do(context.Background()) })

	if err := c.SetHook(hook).Endpoint("http://127.0.0.1:9999", "events").
		Meta("team", "ops").
		Within(key).Detect(Inside).Commands(CommandSet).Bounds(GlobalBounds()).Do(ctx); err != nil {
		t.Fatalf("sethook: %v", err)
	}

	hooks, err := c.Hooks("*").Do(ctx)
	if err != nil {
		t.Fatalf("hooks: %v", err)
	}
	var found *HookInfo
	for i := range hooks {
		if hooks[i].Name == hook {
			found = &hooks[i]
		}
	}
	if found == nil {
		t.Fatalf("hooks = %+v, want it to contain %q", hooks, hook)
	}
	if found.Key != key {
		t.Errorf("hook key = %q, want %q", found.Key, key)
	}
	if len(found.Endpoints) == 0 || found.Endpoints[0] != "http://127.0.0.1:9999/events" {
		t.Errorf("hook endpoints = %v", found.Endpoints)
	}
	// Element 4 of the descriptor is the META the hook was created with; without
	// it a listing cannot tell two hooks on the same key apart.
	if found.Meta["team"] != "ops" {
		t.Errorf("hook meta = %v, want team=ops", found.Meta)
	}

	// NEARBY takes a POINT area and rejects CIRCLE, so this is the only shape a
	// non-roaming NEARBY hook can take.
	nearbyHook := key + "-hook-nearby"
	t.Cleanup(func() { _ = c.DelHook(nearbyHook).Do(context.Background()) })
	if err := c.SetHook(nearbyHook).Endpoint("http://127.0.0.1:9999", "events").
		Nearby(key).Point(33.5, -115.5).Radius(5000).Do(ctx); err != nil {
		t.Fatalf("sethook nearby: %v", err)
	}
	nearbyChan := key + "-chan-nearby"
	t.Cleanup(func() { _ = c.DelChan(nearbyChan).Do(context.Background()) })
	if err := c.SetChan(nearbyChan).Nearby(key).Point(33.5, -115.5).Radius(5000).Do(ctx); err != nil {
		t.Fatalf("setchan nearby: %v", err)
	}

	if err := c.DelHook(hook).Do(ctx); err != nil {
		t.Fatalf("delhook: %v", err)
	}
	if err := c.PDelHook(key + "-*").Do(ctx); err != nil {
		t.Fatalf("pdelhook: %v", err)
	}
}

// A rejected command must not poison the pooled connection.
func TestServerErrorKeepsConnectionUsable(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	_, err := c.Do(ctx, "NEARBY")
	var se ServerError
	if !errors.As(err, &se) {
		t.Fatalf("malformed NEARBY = %v, want a ServerError", err)
	}

	for range 5 {
		if err := c.Ping(ctx); err != nil {
			t.Fatalf("ping after server error: %v", err)
		}
	}
	if err := c.Set(key, "a").Point(1, 1).Do(ctx); err != nil {
		t.Errorf("set after server error: %v", err)
	}
}

func TestConcurrentCommands(t *testing.T) {
	c, key := newClient(t)
	ctx := t.Context()

	const workers = 16
	errs := make(chan error, workers)
	for i := range workers {
		go func() {
			for j := range 20 {
				if err := c.Set(key, fmt.Sprintf("w%d-%d", i, j)).Point(1, 1).Do(ctx); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	for range workers {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent set: %v", err)
		}
	}

	n, err := c.Scan(key).Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != workers*20 {
		t.Errorf("count = %d, want %d", n, workers*20)
	}
}

func waitEvent(t *testing.T, events <-chan *FenceEvent, errs <-chan error) *FenceEvent {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case err := <-errs:
		t.Fatalf("stream ended early: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for a fence event")
	}
	return nil
}
