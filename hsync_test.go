/* Filename convention for test data: "STV"

S: size in bytes.
T: 's' for single or 'd' for duplicates.
V: version

To help identifying files, we set the content to:
- Only one line, no new line.
- Duplicates contain 123...S.
- Unique files contain VVVV... (times S).

Manual tests for source:
- Read errors.
- Stat errors.

Manual tests for target:
- Read errors.
- Stat errors.
*/
package main

import (
	"fmt"
	"testing"
)

func printEntries(entries map[partialHash]fileMatch) {
	hashformat := "%x"
	for k, v := range entries {
		if v.sourceID != nil {
			// `partialHash.hash` is not in hex in the main program, but for
			// convenience we store them in hex here.
			if v.sourceID.h == nil {
				hashformat = "%v"
			}

			if v.targetID != nil && v.targetID != &unsolvable {
				fmt.Printf("%vB %v("+hashformat+"): %v -> %v\n", k.size, k.pos, k.hash, v.targetID.path, v.sourceID.path)
			} else {
				fmt.Printf("%vB %v("+hashformat+"): %v\n", k.size, k.pos, k.hash, v.sourceID.path)
			}
		}
	}
}

func sameEntries(got, want map[partialHash]fileMatch) bool {
	count := 0
	for k, v := range got {
		if v.sourceID != nil {
			count++
			hash := fmt.Sprintf("%x", k.hash)

			w, ok := want[partialHash{size: k.size, pos: k.pos, hash: hash}]
			if !ok || v.sourceID.path != w.sourceID.path ||
				((v.targetID == nil || v.targetID == &unsolvable) && w.targetID != nil) ||
				((v.targetID != nil && v.targetID != &unsolvable) && w.targetID == nil) ||
				(w.targetID != nil && v.targetID.path != w.targetID.path) {
				return false
			}
		}
	}
	if count != len(want) {
		return false
	}
	return true
}

/* Test cases for source:
- Empty files.
- Different sizes.
- Subfolders.
- 2 singles of one size.
- 2 duplicates of one size.
- 3 duplicates of one size in different folders.
- 2 singles and 3 duplicates of one size.

Test cases for target:
- Matching source duplicate.
- Pre-existing conflict with other target file.
- No identical file in source.
- Only 1 match.
- Conflict: Drop different fid and conflict.
- Conflict: Drop fid, keep conflict.
- Conflict: Drop conflict, keep fid.
- Conflict: Drop identical fid and conflict.
*/
func TestVisit(t *testing.T) {
	source := "./testdata/src"
	target := "./testdata/tgt"

	entries := make(map[partialHash]fileMatch)

	visitSource(source, entries)
	visitTarget(target, source, entries)

	// Remove in-place renames.
	for k, v := range entries {
		if v.targetID != nil && v.targetID.path == v.sourceID.path {
			delete(entries, k)
		}
	}

	want := map[partialHash]fileMatch{
		{size: 1}: {sourceID: &fileID{path: "1"}},
		{size: 4, pos: 1, hash: "b59c67bf196a4758191e42f76670ceba"}: {sourceID: &fileID{path: "4s1"}},
		{size: 4, pos: 1, hash: "934b535800b1cba8f96a5d72f72f1611"}: {sourceID: &fileID{path: "sub/4s2"}, targetID: &fileID{path: "folder/4d2"}},
		{size: 5, pos: 1, hash: "b0baee9d279d34fa1dfd71aadb908c3f"}: {sourceID: &fileID{path: "5s1"}},
		{size: 5, pos: 1, hash: "3d2172418ce305c7d16d4b05597c6a59"}: {sourceID: &fileID{path: "5s2"}},
		{size: 6, pos: 1, hash: "96e79218965eb72c92a549dd5a330112"}: {sourceID: &fileID{path: "6"}},
	}

	if !sameEntries(entries, want) {
		t.Errorf("Structure does not match.")
		fmt.Println("==> Got:")
		printEntries(entries)
		fmt.Println("==> Want:")
		printEntries(want)
	}
}
