// Module bench keeps the third-party comparison out of the library's own
// dependency graph: xxhaste itself imports nothing outside the standard
// library.
module github.com/JohanLindvall/xxhaste/bench

go 1.22

replace github.com/JohanLindvall/xxhaste => ../

require (
	github.com/JohanLindvall/xxhaste v0.0.0-00010101000000-000000000000
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/zeebo/xxh3 v1.1.0
)

require (
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	golang.org/x/sys v0.30.0 // indirect
)
