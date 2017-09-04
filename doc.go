// Copyright © 2015-2016 Pierre Neidhardt <ambrevar@gmail.com>
// Use of this file is governed by the license that can be found in LICENSE.

/*
A filesystem hierarchy synchronizer

Rename files in TARGET so that identical files found in SOURCE and TARGET have
the same relative path.

The main goal of the program is to make folders synchronization faster by
sparing big file transfers when a simple rename suffices. It complements other
synchronization programs that lack this capability.

See http://ambrevar.bitbucket.io/hsync and 'hsync -h' for more details.

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
