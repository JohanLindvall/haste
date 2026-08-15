package xxhaste

import "testing"

// The fixed vectors pin a few hundred lengths of one buffer under four seeds.
// Everything else about the implementation is inferred from them, and these
// targets are what test the inference: each states a property that must hold
// for any input, and lets the fuzzer look for the input where it does not.
//
// Their seed corpora run as ordinary tests under `go test`, so a regression
// that the corpus already covers fails the normal suite. Finding new ones
// needs an explicit run:
//
//	go test -run FuzzStreamingMatchesOneShot -fuzz FuzzStreamingMatchesOneShot

// FuzzStreamingMatchesOneShot is the property the whole streaming state machine
// rests on. The chunk size comes out of the corpus too, so the fuzzer can find
// a split that the fixed chunkings in TestStreamingDenseLengths do not make.
func FuzzStreamingMatchesOneShot(f *testing.F) {
	f.Add([]byte("xxhaste"), uint64(0), uint8(0))
	f.Add(testBuffer(300), uint64(1), uint8(0))
	f.Add(testBuffer(1000), uint64(0), uint8(63))
	f.Add(testBuffer(2000), uint64(0x9E3779B185EBCA87), uint8(254))
	f.Add(testBuffer(5000), uint64(^uint64(0)), uint8(191))

	f.Fuzz(func(t *testing.T, in []byte, seed uint64, chunk uint8) {
		k := int(chunk) + 1 // chunk is a size, and zero is not one

		d := NewSeed(seed)
		writeInChunks(d, in, k)
		if got, want := d.Sum64(), Sum64Seed(in, seed); got != want {
			t.Fatalf("len=%d seed=%#x chunk=%d: Sum64 %#016x != %#016x",
				len(in), seed, k, got, want)
		}
		if got, want := d.Sum128(), Sum128Seed(in, seed); got != want {
			t.Fatalf("len=%d seed=%#x chunk=%d: Sum128 %v != %v",
				len(in), seed, k, got, want)
		}

		// Reset has to put the Digest back where New left it, or a reused
		// hasher drifts in a way a single-use one never would.
		d.Reset()
		writeInChunks(d, in, k)
		if got, want := d.Sum64(), Sum64Seed(in, seed); got != want {
			t.Fatalf("len=%d seed=%#x chunk=%d: after Reset %#016x != %#016x",
				len(in), seed, k, got, want)
		}
	})
}

// FuzzCustomSecret fuzzes the secret as well as the input. Secret length is
// what decides the block length, and a length whose secretLimit is not a
// multiple of secretConsumeRate is the case the reference vectors were extended
// to cover; here the fuzzer picks it.
func FuzzCustomSecret(f *testing.F) {
	f.Add(testBuffer(1000), testSecret(MinSecretSize), uint8(0))
	f.Add(testBuffer(4096), testSecret(137), uint8(63))
	f.Add(testBuffer(9000), testSecret(193), uint8(255))
	f.Add(testBuffer(20), testSecret(1024), uint8(7))

	f.Fuzz(func(t *testing.T, in, secret []byte, chunk uint8) {
		// Stretch a short secret rather than skipping it, so no input is
		// wasted; the fuzzer's own bytes stay as the prefix.
		if len(secret) < MinSecretSize {
			secret = append(secret, testSecret(MinSecretSize-len(secret))...)
		}
		k := int(chunk) + 1

		d := NewSecret(secret)
		writeInChunks(d, in, k)
		if got, want := d.Sum64(), Sum64Secret(in, secret); got != want {
			t.Fatalf("len=%d secretLen=%d chunk=%d: Sum64 %#016x != %#016x",
				len(in), len(secret), k, got, want)
		}
		if got, want := d.Sum128(), Sum128Secret(in, secret); got != want {
			t.Fatalf("len=%d secretLen=%d chunk=%d: Sum128 %v != %v",
				len(in), len(secret), k, got, want)
		}
	})
}

// FuzzBackendsAgree runs one input through every kernel this machine can
// execute. On a purego build, and on a machine with only its baseline vector
// unit, there is a single candidate and the target degenerates to a smoke test.
func FuzzBackendsAgree(f *testing.F) {
	f.Add([]byte(nil), uint64(0))
	f.Add(testBuffer(241), uint64(0))
	f.Add(testBuffer(1025), uint64(42))
	f.Add(testBuffer(9000), uint64(0x9E3779B185EBCA87))

	f.Fuzz(func(t *testing.T, in []byte, seed uint64) {
		selected := Backend()
		defer setBackend(selected)

		first := ""
		var want64 uint64
		var want128 Uint128
		for _, name := range candidateBackends() {
			if !setBackend(name) {
				continue
			}
			got64, got128 := Sum64Seed(in, seed), Sum128Seed(in, seed)
			if first == "" {
				first, want64, want128 = name, got64, got128
				continue
			}
			if got64 != want64 {
				t.Fatalf("len=%d seed=%#x: %s %#016x != %s %#016x",
					len(in), seed, name, got64, first, want64)
			}
			if got128 != want128 {
				t.Fatalf("len=%d seed=%#x: %s %v != %s %v",
					len(in), seed, name, got128, first, want128)
			}
		}
	})
}

// FuzzUnmarshalBinary is a robustness target rather than a correctness one.
// Whatever it is handed, UnmarshalBinary must either reject it and leave the
// Digest alone, or accept it and leave one that still behaves like a Digest --
// and must not panic doing either.
func FuzzUnmarshalBinary(f *testing.F) {
	d := New()
	d.Write(testBuffer(700))
	state, err := d.MarshalBinary()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(state)
	f.Add([]byte(magic))
	f.Add([]byte(nil))
	f.Add(make([]byte, marshaledSize))

	empty := Sum64(nil)

	check := func(t *testing.T, b []byte) {
		e := New()
		if err := e.UnmarshalBinary(b); err != nil {
			if got := e.Sum64(); got != empty {
				t.Fatalf("rejected state disturbed the Digest: %#016x != %#016x", got, empty)
			}
			return
		}
		// Accepted: everything below has to complete without panicking, and
		// Reset has to return the Digest to a usable state whatever it held.
		e.Write([]byte("more"))
		e.Sum64()
		e.Sum128()
		e.Sum(nil)
		e.Reset()
		if got := e.Sum64(); got != empty {
			t.Fatalf("Reset after unmarshal: %#016x != %#016x", got, empty)
		}
	}

	f.Fuzz(func(t *testing.T, b []byte) {
		check(t, b)

		// Nearly every random input fails on the length or the magic and never
		// reaches the field checks. Splicing the same bytes into a well-formed
		// envelope is what puts the accepting path under the fuzzer as well.
		env := make([]byte, marshaledSize)
		copy(env, magic)
		copy(env[len(magic):], b)
		check(t, env)
	})
}
