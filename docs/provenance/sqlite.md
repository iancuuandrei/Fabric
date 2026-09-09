# SQLite journal dependency decision

Status: implemented as the default for the first successful append to a missing
path. Existing JSONL remains on its original backend. Evidence checked
2026-09-08.

## Selection

The journal uses `modernc.org/sqlite` v1.58.0 through `database/sql`. The module
is a CGo-free port of SQLite, is tagged and stable, supports Windows amd64 and
arm64, and is licensed BSD-3-Clause. Its v1.58.0 changelog identifies embedded
SQLite 3.53.4 and explicitly requires downstream users to pin the same
`modernc.org/libc` version as its module file. This repository therefore pins:

| Module | Version | Go module sum |
| --- | --- | --- |
| `modernc.org/sqlite` | `v1.58.0` | `h1:38u40/bwkfM7f0Myhosl+SEMltSDxnGdQf8o6Kjmys0=` |
| `modernc.org/libc` | `v1.75.6` | `h1:yKk8qo+Di4gkmvRboK8ocCqH22FiUCR6jRy2OwtCRus=` |

Primary evidence:

- [`modernc.org/sqlite` v1.58.0 package record](https://pkg.go.dev/modernc.org/sqlite@v1.58.0)
- [v1.58.0 changelog](https://gitlab.com/cznic/sqlite/-/blob/v1.58.0/CHANGELOG.md)
- [v1.58.0 module file](https://gitlab.com/cznic/sqlite/-/blob/v1.58.0/go.mod)
- [SQLite 3.53.4 release](https://sqlite.org/releaselog/3_53_4.html)
- [SQLite atomic commit description](https://sqlite.org/atomiccommit.html)

The resolved module-cache license evidence is:

| Module/file | Bytes | SHA-256 |
| --- | ---: | --- |
| `modernc.org/sqlite@v1.58.0/LICENSE` | 1489 | `c6fe05491a60ae13bcd223088d2705e36dede24e5587226231d2459ada5c4822` |
| `modernc.org/sqlite@v1.58.0/LICENSE-SQLITE` | 1506 | `8438c9c89b849131ead81d5435cb97fcf052df5b0b286dda8a2d4c29e6cb3fd0` |
| `modernc.org/sqlite@v1.58.0/LICENSE-SQLITE_VEC` | 1068 | `6ce72bbe12d975bd5286e5ab0a064c069693300c47bccbc57bec18485f1621ea` |
| `modernc.org/libc@v1.75.6/LICENSE` | 1482 | `95ff867eb55a56935fa7492406cfa953fb7c13ca73f4c0a86ae05756b4605600` |
| `modernc.org/libc@v1.75.6/LICENSE-3RD-PARTY.md` | 10505 | `f597097efe3d97021f89170746bd3a0fb9a8b6fb26b82043ed68a4e0283bee6c` |

These hashes identify locally resolved evidence for packaging review. They do
not by themselves establish that all target-specific linked code or license
obligations have been reviewed.

## Alternatives considered

`github.com/mattn/go-sqlite3` is mature and MIT-licensed, but requires CGo. That
conflicts with the requested pure-Go Windows packaging path. The maintained MIT
`github.com/ncruces/go-sqlite3` is also CGo-free, but executes SQLite through a
WebAssembly-derived runtime and was pre-v1 at review time. It is suitable for
many applications; the additional runtime layer was unnecessary for this local
`database/sql` journal. A custom JSONL durability engine was rejected because
file locking, transactions, recovery and indexing are outside the project's
orchestration thesis.

## Preserved semantics and limits

SQLite owns atomic transactions, locking and crash recovery. Connections request
WAL mode, `synchronous=FULL`, a ten-second busy timeout, immediate write
transactions and the driver's defensive mode. The journal package still owns
canonical JSON, the `harness.event.v1` SHA-256 chain, contiguous sequencing,
the 1 MiB event bound, the 64 MiB journal bound and the caller's semantic
validator. Database constraints and triggers also reject noncontiguous inserts,
wrong previous hashes, updates and deletes.

Append reads and validates the complete chain, constructs the next event, and
runs the semantic validator inside the same immediate transaction as the insert.
The validator receives a deep copy, so it cannot mutate or retain payload bytes
that later become durable. A pre-commit failure rolls back. Any error returned by
`Commit` is classified as `ErrCommitUncertain`; callers must read and reconcile
before retrying because an error cannot prove which side of the commit boundary
became durable.

Import validates the complete canonical JSONL chain before opening its write
transaction and refuses a nonempty target. It never deletes or replaces the old
JSONL file. Export reconstructs canonical, newline-terminated JSONL after a full
database validation. Round-trip tests compare the exported bytes exactly with
the original JSONL, including sequence and event hashes.

The package-level dispatcher recognizes SQLite only from the exact file header;
file extensions do not select storage. Reads of missing paths remain empty and
do not create a database. Existing empty, canonical, torn or corrupt non-SQLite
files retain strict JSONL behavior, and a legacy `.lock` beside a missing path
remains an explicit recovery stop. First creation builds and commits the initial
event in a same-directory temporary SQLite database, closes it, then publishes
the database with a no-replace hard link. Concurrent creators either publish one
complete database or retry against that complete database; they never inspect a
partially initialized target. Existing JSONL is never imported automatically.
SQLite inspection uses `mode=ro`, verifies schema version 1, and does not run
schema initialization or persistent write PRAGMAs. Unsupported schemas fail.

Executed local tests cover exact JSONL round trips, corrupt and conflicting
imports, validator rollback, validator mutation and retention, commit-error
classification, concurrent writers through independent handles, database
sequence constraints, stored-data tampering, and child-process termination on
both sides of the commit boundary. These are local Windows observations, not a
claim about power-loss behavior, other filesystems, or other operating systems.
