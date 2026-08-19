// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

// Command entry points. Each returns a builder; see the builder's terminal
// method for how the command is executed.

// Set starts building a Tile38 SET command for the given collection and object ID.
func (c *Client) Set(collection, id string) *SetCmd {
	return &SetCmd{c: c, args: []any{"SET", collection, id}}
}

// Del starts building a Tile38 DEL command for the given collection and object ID.
func (c *Client) Del(collection, id string) *DelCmd {
	return &DelCmd{c: c, args: []any{"DEL", collection, id}}
}

// FSet starts building a Tile38 FSET command for the given collection and object ID.
func (c *Client) FSet(collection, id string) *FSetCmd {
	return &FSetCmd{c: c, args: []any{"FSET", collection, id}}
}

// FGet starts building a Tile38 FGET command to read a single field value.
func (c *Client) FGet(collection, id, field string) *FGetCmd {
	return &FGetCmd{c: c, args: []any{"FGET", collection, id, field}}
}

// FExists starts building a Tile38 FEXISTS command to test whether a field is
// set on an object.
func (c *Client) FExists(collection, id, field string) *FExistsCmd {
	return &FExistsCmd{c: c, args: []any{"FEXISTS", collection, id, field}}
}

// Get starts building a Tile38 GET query for the given collection and object ID.
func (c *Client) Get(collection, id string) *GetCmd {
	return &GetCmd{c: c, args: []any{"GET", collection, id}}
}

// Nearby starts building a Tile38 NEARBY query for the given collection.
func (c *Client) Nearby(collection string) *NearbyCmd[string] {
	return &NearbyCmd[string]{
		searchState: &searchState{c: c, verb: "NEARBY", args: []any{"NEARBY", collection}},
		out:         formatIDs,
	}
}

// Scan starts building a Tile38 SCAN query for the given collection.
func (c *Client) Scan(collection string) *ScanCmd[string] {
	return &ScanCmd[string]{
		searchState: &searchState{c: c, verb: "SCAN", args: []any{"SCAN", collection}},
		out:         formatIDs,
	}
}

// Search starts building a Tile38 SEARCH query, which matches on the string
// values "SET … STRING" stores rather than on geometry.
func (c *Client) Search(collection string) *SearchCmd[string] {
	return &SearchCmd[string]{
		searchState: &searchState{c: c, verb: "SEARCH", args: []any{"SEARCH", collection}},
		out:         formatIDs,
	}
}

// Test starts building a Tile38 TEST command comparing area against another,
// given to Within or Intersects. It touches no stored object.
func (c *Client) Test(area Area) *TestCmd {
	return &TestCmd{c: c, area1: area}
}

// Within starts building a Tile38 WITHIN query for the given collection.
func (c *Client) Within(collection string) *WithinCmd[string] {
	return &WithinCmd[string]{
		searchState: &searchState{c: c, verb: "WITHIN", args: []any{"WITHIN", collection}},
		out:         formatIDs,
	}
}

// Intersects starts building a Tile38 INTERSECTS query for the given collection.
func (c *Client) Intersects(collection string) *IntersectsCmd[string] {
	return &IntersectsCmd[string]{
		searchState: &searchState{c: c, verb: "INTERSECTS", args: []any{"INTERSECTS", collection}},
		out:         formatIDs,
	}
}

// SetHook starts building a Tile38 SETHOOK command for the given hook name.
func (c *Client) SetHook(hookName string) *HookCmd {
	return &HookCmd{c: c, name: hookName}
}

// DelHook starts building a Tile38 DELHOOK command for the given hook name.
func (c *Client) DelHook(hookName string) *DelHookCmd {
	return &DelHookCmd{c: c, args: []any{"DELHOOK", hookName}}
}

// PDelHook starts building a Tile38 PDELHOOK command (pattern-based hook deletion).
func (c *Client) PDelHook(pattern string) *PDelHookCmd {
	return &PDelHookCmd{c: c, args: []any{"PDELHOOK", pattern}}
}

// Hooks starts building a Tile38 HOOKS command to list registered hooks.
// Pass "*" to list all hooks.
func (c *Client) Hooks(pattern string) *HooksCmd {
	return &HooksCmd{c: c, args: []any{"HOOKS", pattern}}
}

// Drop starts building a Tile38 DROP command to delete an entire collection.
func (c *Client) Drop(collection string) *DropCmd {
	return &DropCmd{c: c, args: []any{"DROP", collection}}
}

// PDel starts building a Tile38 PDEL command to delete objects matching a glob pattern.
func (c *Client) PDel(collection, pattern string) *PDelCmd {
	return &PDelCmd{c: c, args: []any{"PDEL", collection, pattern}}
}

// Rename starts building a Tile38 RENAME command. Chain NX() to use RENAMENX.
func (c *Client) Rename(collection, newCollection string) *RenameCmd {
	return &RenameCmd{c: c, args: []any{"RENAME", collection, newCollection}}
}

// Expire starts building a Tile38 EXPIRE command to set a TTL on an object.
func (c *Client) Expire(collection, id string, seconds int) *ExpireCmd {
	return &ExpireCmd{c: c, args: []any{"EXPIRE", collection, id, seconds}}
}

// Persist starts building a Tile38 PERSIST command to remove the TTL from an object.
func (c *Client) Persist(collection, id string) *PersistCmd {
	return &PersistCmd{c: c, args: []any{"PERSIST", collection, id}}
}

// TTL starts building a Tile38 TTL command to read the remaining TTL of an object.
func (c *Client) TTL(collection, id string) *TTLCmd {
	return &TTLCmd{c: c, args: []any{"TTL", collection, id}}
}

// Exists starts building a Tile38 EXISTS command to check whether an object exists.
func (c *Client) Exists(collection, id string) *ExistsCmd {
	return &ExistsCmd{c: c, args: []any{"EXISTS", collection, id}}
}

// Keys starts building a Tile38 KEYS command to list collection names matching a glob pattern.
// Pass "*" to list all collections.
func (c *Client) Keys(pattern string) *KeysCmd {
	return &KeysCmd{c: c, args: []any{"KEYS", pattern}}
}

// Bounds starts building a Tile38 BOUNDS command to get the bounding box of a collection.
func (c *Client) Bounds(collection string) *BoundsCmd {
	return &BoundsCmd{c: c, args: []any{"BOUNDS", collection}}
}

// DBSize starts building a query for the total object count across all
// collections. Tile38 has no DBSIZE command, so this reads num_objects from
// SERVER.
func (c *Client) DBSize() *DBSizeCmd {
	return &DBSizeCmd{c: c, args: []any{"SERVER"}}
}

// FlushDB starts building a Tile38 FLUSHDB command to delete all objects and collections.
func (c *Client) FlushDB() *FlushDBCmd {
	return &FlushDBCmd{c: c, args: []any{"FLUSHDB"}}
}

// SetChan starts building a Tile38 SETCHAN command for a pub/sub geofence channel.
func (c *Client) SetChan(channelName string) *SetChanCmd {
	return &SetChanCmd{c: c, name: channelName}
}

// DelChan starts building a Tile38 DELCHAN command.
func (c *Client) DelChan(channelName string) *DelChanCmd {
	return &DelChanCmd{c: c, args: []any{"DELCHAN", channelName}}
}

// PDelChan starts building a Tile38 PDELCHAN command (pattern-based channel deletion).
func (c *Client) PDelChan(pattern string) *PDelChanCmd {
	return &PDelChanCmd{c: c, args: []any{"PDELCHAN", pattern}}
}

// Chans starts building a Tile38 CHANS command to list registered pub/sub channels.
// Pass "*" to list all channels.
func (c *Client) Chans(pattern string) *ChansCmd {
	return &ChansCmd{c: c, args: []any{"CHANS", pattern}}
}

// JSet starts building a Tile38 JSET command to set a JSON field by path.
func (c *Client) JSet(collection, id, path string, value any) *JSetCmd {
	return &JSetCmd{c: c, args: []any{"JSET", collection, id, path, value}}
}

// JGet starts building a Tile38 JGET command to read a JSON field by path.
func (c *Client) JGet(collection, id, path string) *JGetCmd {
	return &JGetCmd{c: c, args: []any{"JGET", collection, id, path}}
}

// JDel starts building a Tile38 JDEL command to delete a JSON field by path.
func (c *Client) JDel(collection, id, path string) *JDelCmd {
	return &JDelCmd{c: c, args: []any{"JDEL", collection, id, path}}
}
