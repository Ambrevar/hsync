// Copyright © 2015 Pierre Neidhardt <ambrevar at gmail dot com>
// Use of this file is governed by the license that can be found in LICENSE.

/*
A filesystem hierarchy synchronizer

Rename files in TARGET so that identical files found in SOURCE and TARGET have
the same relative path.

The main goal of the program is to make folders synchronization faster by
sparing big file transfers when a simple rename suffices. It complements other
synchronization programs that lack this capability.

See http://ambrevar.bitbucket.org/hsync and 'hsync -h' for more details.

Usage:

	hsync [OPTIONS] SOURCE TARGET

For usage options, see:

	hsync -h

Implementation details

We store the file entries in the following structure:

	entries := map[partialHash struct{size int64, pos int64, hash string}]fileMatch struct{
		sourceID *fileID{path string, h hash.Hash},
		targetID *fileID{path string, h hash.Hash}
	}

This 'entries' map indexes the possible file matches by content ('partialHash').
A file match references the paths that will be used to rename the file in TARGET
from 'oldpath' to 'newpath'. Note that 'newpath' is given by 'sourceID.path',
and 'oldpath' by 'targetID.path'.

The algorithm is centered around one main optimization: rolling-checksums. We
assume that two files match if they have the same partial hash.

The initial partial hash is just the size with an empty hash. This speeds up the
process since this saves an open/close of the file. We just need a 'stat'. Files
will not be read unless a rolling-checksum is required. As a consequence,
unreadable files with a unique size will be stored in 'entries', while
unreadable conflicting files will be discarded. Note that the system allows to
rename files that cannot be read.

One checksum roll increments 'pos' and updates the hash by hashing the next
BLOCKSIZE bytes of the file. BLOCKSIZE is set to a value that is commonly
believed to be optimal in most cases. The optimal value would be the device
blocksize where the file resides. It would be more complex and memory consuming
to query this value for each file.

We choose md5 (128 bits) as the checksum algorithm. Adler32, CRC-32 and CRC-64
are only a tiny little faster while suffering from more clashes. This choice
should be backed up with a proper benchmark.

A conflict arises when two files in either SOURCE or TARGET have the same
partial hash. We solve the conflict by updating the partial hashes until they
differ. If the partial hashes cannot be updated any further (i.e. we reached
end-of-file), it means that the files are duplicates.

Notes:

- Partial hashes of conflicting files will be complete at the same roll since
they have the same size.

- When a partial hash is complete, we have the following relation:

	(pos-1)*BLOCKSIZE < filesize <= pos*BLOCKSIZE

- There is only one possible conflicting file at a time.

A file match may be erroneous if the partial hash is not complete. The most
obvious case is when two different files are the only ones of size N in SOURCE
and TARGET. This down-side is a consequence of the design choice, i.e. focus on
speed. Erroneous matches can be corrected in the preview file. If we wanted no
ambiguity, we would have to compute the full hashes and this would take
approximately as much time as copying files from SOURCE to TARGET, like a
regular synchronization tool would do.

We store the digest 'hash.Hash' together with the file path for when we update a
partial hash.

Process:

1. We walk SOURCE completely. Only regular files are processed. The 'sourceID'
are stored. If two entries conflict (they have the same partial hash), we
compute update the partial hashes until they do not conflict anymore. If the
conflict is not resolvable, i.e. the partial hash is complete and files are
identical, we drop both files from 'entries'.

Future files can have the same partial hash that led to a former conflict. To
distinguish the content from former conflicts when adding a new file, we must
compute the partial hash up to the 'pos' of the last conflict (the number of
checksum rolls). To keep track of this 'pos' when there is a conflict, we mark
all computed partial hash as dummy values. When the next entry will be added, we
will have to compute the partial hash until it does not match a dummy value in
'entries'.

Duplicates are not processed but display a warning. Usually the user does not
want duplicates, so she is better off fixing them before processing with the
renames. It would add a lot of complexity to handle duplicates properly.

2. We walk TARGET completely. We skip all dummies as source the SOURCE walk.
We need to analyze SOURCE completely before we can check for matches.

- If there are only dummy entries, there was an unsolvable conflict in SOURCE.
We drop the file.

- If we end on a non-empty entry with an 'unsolvable' targetID, it means that an
unsolvable conflict with target files happened with this partial hash. This is
only possible at end-of-file. We drop the file.

- If we end on an empty entry, there is no match with SOURCE and we drop the
file.

- If we end on a non-empty entry without previous matches, we store the
match.

- Else we end on a non-empty entry with one match already present. This is a
conflict. We solve the conflict as for the SOURCE walk except that we need to
update the partial hashes of three files: the SOURCE file, the first TARGET
match and the new TARGET match.

3. We generate the 'renameOps' and 'reverseOps' maps. They map 'oldpath' to
'newpath' and 'newpath' to 'oldpath' respectively. We drop entries where
'oldpath==newpath' to spare a lot of noise.

Note that file names are not used to compute a match since they could be
identical while the content would be different.

4. We proceed with the renames. Chains and cycles may occur.

- Example of a chain of renames: a->b, b->c, c->d.

- Example of a cycle of renames: a->b, b->c, c->a.

TARGET must be fully analyzed before proceeding with the renames so that we can
detect chains.

We always traverse chains until we reach the end, then rename the elements while
going backward till the beginning. The beginning can be before the entry point.
'reverseOps' is used for going backward.

When a cycle is detected, we break it down to a chain. We rename one file to a
temporary name. Then we add this new file to the other end of the chain so that
it gets renamed to its original new name once all files have been processed.
*/
package main

import (
	"crypto/md5"
	"encoding/json"
	"flag"
	"fmt"
	"hash"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
)

const (
	APPLICATION = "hsync"
	VERSION     = "1.1"
	COPYRIGHT   = "Copyright (C) 2015 Pierre Neidhardt"
	BLOCKSIZE   = 4096
	SEPARATOR   = string(os.PathSeparator)
)

var usage = `Filesystem hierarchy synchronizer

Rename files in TARGET so that identical files found in SOURCE and TARGET have
the same relative path.

The main goal of the program is to make folders synchronization faster by
sparing big file transfers when a simple rename suffices. It complements other
synchronization programs that lack this capability.

By default, files are not renamed and a preview is printed to standard output.

False positives can happen, e.g. if two different files in SOURCE and TARGET are
the only ones of this size. Use the preview to spot false positives and make sure
all files get renamed properly.

You can redirect the preview to a file. If you run the program using this
preview file as SOURCE, the analysis will be skipped. This is useful if you want
to tweak the result of the analysis.

Notes:
- Duplicate files in either folder are skipped.
- Only regular files are processed. In particular, empty folders and symbolic
links are ignored.
`

// We attach a hash digest to the path so that we can update partial hashes with
// the rolling-checksum function.
type fileID struct {
	path string
	h    hash.Hash
}

var unsolvable = fileID{path: SEPARATOR}

// A fileMatch stores 2 fileID with matching content. A match can be partial and
// further processing can disprove it.
// - If 'sourceID==nil', this is a dummy match. It means that a file of the same
// size with a longer partialHash has been processed.
// - If 'targetID==nil', a match is yet to be found.
// - If 'targetID==&unsolvable', several TARGET files conflict together for this
// SOURCE file, the entry should be skipped.
type fileMatch struct {
	sourceID *fileID
	targetID *fileID
}

// partialHash is used as a key to identify the file content.
// The 'size' field should always be set, however the 'pos' and 'hash' fields
// are computed only when required. No hash has been computed when 'pos==0'.
type partialHash struct {
	size int64
	pos  int64
	hash string
}

// rollingChecksum returns io.EOF on last roll.
// The caller needs not open `file`; it needs to close it however. This manual
// management avoids having to open and close the file repeatedly.
func rollingChecksum(fid *fileID, key *partialHash, file **os.File) (err error) {
	if *file == nil {
		*file, err = os.Open(fid.path)
		if err != nil {
			return
		}
	}

	buf := [BLOCKSIZE]byte{}
	n, err := (*file).ReadAt(buf[:], key.pos*BLOCKSIZE)
	if err != nil && err != io.EOF {
		return
	}
	fid.h.Write(buf[:n])
	key.pos++
	key.hash = string(fid.h.Sum(nil))
	return
}

func newFileEntry(path string, size int64) (fileID, partialHash) {
	return fileID{path: path, h: md5.New()}, partialHash{size: size}
}

func visitSource(root string, entries map[partialHash]fileMatch) {
	// Change folder to 'root' so that 'root' does not get stored in fileID.path.
	oldroot, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	err = os.Chdir(root)
	if err != nil {
		log.Fatal(err)
	}
	defer os.Chdir(oldroot)

	visitor := func(input string, info os.FileInfo, ignored error) error {
		if !info.Mode().IsRegular() {
			return nil
		}

		inputID, inputKey := newFileEntry(input, info.Size())
		var err error

		var inputFile, conflictFile *os.File
		defer func() {
			if inputFile != nil {
				inputFile.Close()
			}
		}()
		defer func() {
			if conflictFile != nil {
				conflictFile.Close()
			}
		}()

		// Skip dummy matches.
		v, ok := entries[inputKey]
		for ok && v.sourceID == nil && err != io.EOF {
			err = rollingChecksum(&inputID, &inputKey, &inputFile)

			if err != nil && err != io.EOF {
				log.Println(err)
				return nil
			}
			v, ok = entries[inputKey]
		}

		if ok && v.sourceID == nil {
			log.Printf("Source duplicate (%x) '%v'\n", inputKey.hash, inputID.path)
			return nil
		} else if !ok {
			entries[inputKey] = fileMatch{sourceID: &inputID}
			return nil
		}

		// Else there is a conflict.
		conflictKey := inputKey
		conflictID := entries[inputKey].sourceID

		for inputKey == conflictKey && err == nil {
			// Set dummy value to mark the key as visited for future files.
			entries[inputKey] = fileMatch{}

			err = rollingChecksum(&inputID, &inputKey, &inputFile)
			if err != nil && err != io.EOF {
				// Read error. Drop input.
				log.Println(err)
				return nil
			}

			err = rollingChecksum(conflictID, &conflictKey, &conflictFile)
			if err != nil && err != io.EOF {
				// Read error. We will replace conflict with input.
				log.Println(err)
				break
			}
		}

		if inputKey == conflictKey && err == io.EOF {
			entries[inputKey] = fileMatch{}
			log.Printf("Source duplicate (%x) '%v'\n", inputKey.hash, inputID.path)
			log.Printf("Source duplicate (%x) '%v'\n", conflictKey.hash, conflictID.path)
		} else {
			// Resolved conflict.
			entries[inputKey] = fileMatch{sourceID: &inputID}
			if err == nil || err == io.EOF {
				// Re-add conflicting file except on read error.
				entries[conflictKey] = fileMatch{sourceID: conflictID}
			}
		}

		return nil
	}

	// Since we do not stop on read errors while walking, the returned error is
	// always nil.
	filepath.Walk(".", visitor)
}

// See comments in visitSource.
func visitTarget(root, sourceRoot string, entries map[partialHash]fileMatch) {
	oldroot, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	err = os.Chdir(root)
	if err != nil {
		log.Fatal(err)
	}
	defer os.Chdir(oldroot)

	visitor := func(input string, info os.FileInfo, ignored error) error {
		if !info.Mode().IsRegular() {
			return nil
		}

		inputID, inputKey := newFileEntry(input, info.Size())
		var err error

		var inputFile, conflictFile, sourceFile *os.File
		defer func() {
			if inputFile != nil {
				inputFile.Close()
			}
		}()
		defer func() {
			if conflictFile != nil {
				conflictFile.Close()
			}
		}()
		defer func() {
			if sourceFile != nil {
				sourceFile.Close()
			}
		}()

		// Skip dummy matches.
		v, ok := entries[inputKey]
		for ok && v.sourceID == nil && err != io.EOF {
			err = rollingChecksum(&inputID, &inputKey, &inputFile)
			if err != nil && err != io.EOF {
				log.Println(err)
				return nil
			}
			v, ok = entries[inputKey]
		}

		if ok && v.sourceID == nil {
			log.Printf("Target duplicate match (%x) '%v'\n", inputKey.hash, inputID.path)
			return nil
		} else if ok && v.targetID != nil && v.targetID == &unsolvable {
			// Unresolved conflict happened previously.
			log.Printf("Target duplicate (%x) '%v', source match '%v'\n", inputKey.hash, inputID.path, v.sourceID.path)
			return nil
		} else if !ok {
			// No matching file in source.
			return nil
		} else if v.targetID == nil {
			// First match.
			entries[inputKey] = fileMatch{sourceID: entries[inputKey].sourceID, targetID: &inputID}
			return nil
		}

		// Else there is a conflict.
		sourceKey := inputKey
		sourceID := entries[inputKey].sourceID

		conflictKey := inputKey
		conflictID := entries[inputKey].targetID

		for inputKey == conflictKey && inputKey == sourceKey && err == nil {
			// Set dummy value to mark the key as visited for future files.
			entries[inputKey] = fileMatch{}

			// Since we change folders, we don't have to store the root in fileID, nor
			// we have to compute sourceRoot's realpath to open the file from this
			// point.
			os.Chdir(oldroot)
			os.Chdir(sourceRoot)
			err = rollingChecksum(sourceID, &sourceKey, &sourceFile)
			os.Chdir(oldroot)
			os.Chdir(root)
			if err != nil && err != io.EOF {
				// Read error. Drop all entries.
				log.Println(err)
				return nil
			}

			err = rollingChecksum(&inputID, &inputKey, &inputFile)
			inputErr := err
			if err != nil && err != io.EOF {
				// Read error. Drop input.
				log.Println(err)
				// We don't break now as there is still a chance that the conflicting
				// file matches the source.
			}

			err = rollingChecksum(conflictID, &conflictKey, &conflictFile)
			if err != nil && err != io.EOF {
				// Read error. We will replace conflict with input if the latter has
				// been read correctly.
				log.Println(err)
				break
			}

			if inputErr != nil && inputErr != io.EOF {
				break
			}
		}

		if inputKey == sourceKey && inputKey == conflictKey && err == io.EOF {
			log.Printf("Target duplicate (%x) '%v', source match '%v'\n", inputKey.hash, inputID.path, v.sourceID.path)
			log.Printf("Target duplicate (%x) '%v', source match '%v'\n", conflictKey.hash, conflictID.path, v.sourceID.path)
			// We mark the source file with an unresolved conflict for future target files.
			entries[sourceKey] = fileMatch{sourceID: sourceID, targetID: &unsolvable}
		} else if inputKey == sourceKey && inputKey != conflictKey {
			// Resolution: drop conflicting entry.
			entries[sourceKey] = fileMatch{sourceID: sourceID, targetID: &inputID}
		} else if conflictKey == sourceKey && conflictKey != inputKey {
			// Resolution: drop input entry.
			entries[sourceKey] = fileMatch{sourceID: sourceID, targetID: conflictID}
		} else if conflictKey != sourceKey && inputKey != sourceKey {
			// Resolution: drop both entries.
			entries[sourceKey] = fileMatch{sourceID: sourceID}
		}
		// Else we drop all entries.

		return nil
	}

	filepath.Walk(".", visitor)
}

// Rename files as specified in renameOps.
// Chains and cycles may occur. See the implementation details.
func processRenames(root string, renameOps, reverseOps map[string]string, clobber bool) {
	// Change folder since the renames are made relatively to 'root'.
	oldroot, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	err = os.Chdir(root)
	if err != nil {
		log.Fatal(err)
	}
	defer os.Chdir(oldroot)

	for oldpath, newpath := range renameOps {
		if oldpath == newpath {
			continue
		}

		cycleMarker := oldpath

		// Go forward to the end of the chain or the cycle.
		for newpath != cycleMarker {
			_, ok := renameOps[newpath]
			if !ok {
				break
			}
			oldpath = newpath
			newpath = renameOps[newpath]
		}

		// If cycle, break it down to a chain.
		if cycleMarker == newpath {
			f, err := ioutil.TempFile(".", APPLICATION)
			if err != nil {
				log.Fatal(err)
			}
			tmp := f.Name()
			f.Close()

			err = os.Rename(oldpath, tmp)
			if err != nil {
				log.Println(err)
			} else {
				log.Printf("Rename '%v' -> '%v'", oldpath, tmp)
			}

			// Plug temp file to the other end of the chain.
			reverseOps[cycleMarker] = tmp

			// During one loop over 'renameOps', we may process several operations in
			// case of chains and cycles. Remove rename operation so that no other
			// loop over 'renameOps' processes it again.
			delete(renameOps, oldpath)
			// Go backward.
			newpath = oldpath
			oldpath = reverseOps[oldpath]
		}

		// Process the chain of renames. Renaming can still fail, in which case we
		// output the error and go on with the chain.
		for oldpath != "" {
			err = os.MkdirAll(filepath.Dir(newpath), 0777)
			if err != nil {
				log.Println(err)
			} else {
				// There is a race condition between the existance check and the rename.
				// We could create a hard link to rename atomically without overwriting.
				// But 1) we need to remove the original link afterward, so we lose
				// atomicity, 2) hard links are not supported by all filesystems.
				exists := false
				if !clobber {
					_, err = os.Stat(newpath)
					if err == nil || os.IsExist(err) {
						exists = true
					}
				}
				if clobber || !exists {
					err := os.Rename(oldpath, newpath)
					if err != nil {
						log.Println(err)
					} else {
						log.Printf("Rename '%v' -> '%v'", oldpath, newpath)
					}
				} else {
					log.Printf("Destination exists, skip renaming: '%v' -> '%v'", oldpath, newpath)
				}
			}

			delete(renameOps, oldpath)
			newpath = oldpath
			oldpath = reverseOps[oldpath]
		}
	}
}

func init() {
	log.SetFlags(0)
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %v SOURCE TARGET\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, usage)
		fmt.Fprintln(os.Stderr, "Options:")
		flag.PrintDefaults()
	}

	var flagClobber = flag.Bool("f", false, "Overwrite existing files in TARGETS.")
	var flagProcess = flag.Bool("p", false, "Rename the files in TARGETS.")
	var flagVersion = flag.Bool("v", false, "Print version and exit.")
	flag.Parse()
	if *flagVersion {
		fmt.Println(APPLICATION, VERSION, COPYRIGHT)
		return
	}

	if flag.Arg(0) == "" || flag.Arg(1) == "" {
		flag.Usage()
		return
	}

	renameOps := make(map[string]string)
	reverseOps := make(map[string]string)
	s, err := os.Stat(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}

	if s.IsDir() {
		entries := make(map[partialHash]fileMatch)
		log.Printf(":: Analyzing '%v'", flag.Arg(0))
		visitSource(flag.Arg(0), entries)
		log.Printf(":: Analyzing '%v'", flag.Arg(1))
		visitTarget(flag.Arg(1), flag.Arg(0), entries)

		for _, v := range entries {
			if v.targetID != nil && v.targetID != &unsolvable && v.targetID.path != v.sourceID.path {
				renameOps[v.targetID.path] = v.sourceID.path
				reverseOps[v.sourceID.path] = v.targetID.path
			}
		}
	} else {
		buf, err := ioutil.ReadFile(flag.Arg(0))
		if err != nil {
			log.Fatal(err)
		}
		err = json.Unmarshal(buf, &renameOps)
		if err != nil {
			log.Fatal(err)
		}

		for oldpath, newpath := range renameOps {
			if oldpath == newpath {
				delete(renameOps, oldpath)
				continue
			}
			_, err := os.Stat(flag.Arg(1) + SEPARATOR + oldpath)
			if err != nil && os.IsNotExist(err) {
				// Remove non-existing entries.
				delete(renameOps, oldpath)
				continue
			}
			reverseOps[newpath] = oldpath
		}
	}

	if *flagProcess {
		log.Println(":: Processing renames")
		processRenames(flag.Arg(1), renameOps, reverseOps, *flagClobber)
	} else {
		log.Println(":: Previewing renames")
		// There should be no error.
		buf, _ := json.MarshalIndent(renameOps, "", "\t")
		os.Stdout.Write(buf)
		fmt.Println()
	}
}
