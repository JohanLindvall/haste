// Module bench keeps the third-party comparison out of the library's own
// dependency graph: haste itself imports nothing outside the standard
// library.
module github.com/JohanLindvall/haste/bench

go 1.24.2

replace github.com/JohanLindvall/haste => ../

require (
	github.com/JohanLindvall/haste v0.0.0-00010101000000-000000000000
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/vkudryk/rapidhash-go v0.0.0-20250522125531-a03e5539e1bf
	github.com/zeebo/xxh3 v1.1.0
)

require (
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	golang.org/x/sys v0.30.0 // indirect
)
