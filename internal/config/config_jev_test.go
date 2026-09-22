package config

import (
	"bytes"

	"gopkg.in/yaml.v3"
	"strings"
	"testing"
)

// The jev.review_assist pre-brief and its jev.candidate_excerpt_bytes option
// were removed after the offline trial showed the candidate generator excludes
// changed files by construction while nearly all recorded finding locations
// are changed files, so the listing cannot reach what it ranks for. The keys
// are gone from the resolved config and the documentation; the raw decoder
// keeps a tombstone for the `jev:` block only so a global config that still
// sets either key keeps parsing with no effect instead of failing the whole
// document as an unknown field.

// TestRetiredJevKeysParseWithoutEffect pins the retirement contract: a global
// config carrying either removed key (or both, with arbitrary content) loads
// cleanly and merges exactly like a config without the block - there is no
// Jev setting left on GlobalConfig or the merged Config to set.
func TestRetiredJevKeysParseWithoutEffect(t *testing.T) {
	withKeys, err := LoadGlobalFromBytes([]byte("log_level: info\njev:\n  review_assist: true\n  candidate_excerpt_bytes: 1024\n"))
	if err != nil {
		t.Fatalf("global config with retired jev keys must still parse: %v", err)
	}
	withoutKeys, err := LoadGlobalFromBytes([]byte("log_level: info\n"))
	if err != nil {
		t.Fatal(err)
	}
	if withKeys.LogLevel != withoutKeys.LogLevel || withKeys.Eval != withoutKeys.Eval || withKeys.SessionReuse != withoutKeys.SessionReuse || withKeys.Agent != withoutKeys.Agent {
		t.Fatalf("retired jev block changed the parsed config: with = %#v, without = %#v", withKeys, withoutKeys)
	}
	mergedWith := Merge(withKeys, &RepoConfig{})
	mergedWithout := Merge(withoutKeys, &RepoConfig{})
	if mergedWith.Eval != mergedWithout.Eval || mergedWith.LogLevel != mergedWithout.LogLevel || mergedWith.Agent != mergedWithout.Agent {
		t.Fatalf("retired jev block changed the merged config: with = %#v, without = %#v", mergedWith, mergedWithout)
	}
}

// TestRetiredJevTombstoneSwallowsTheBlock decodes the raw schema directly to
// pin the mechanism: the retired block lands in the tombstone and is
// discarded, so even content no removed feature ever defined parses silently.
func TestRetiredJevTombstoneSwallowsTheBlock(t *testing.T) {
	var raw globalConfigRaw
	dec := yaml.NewDecoder(bytes.NewReader([]byte("jev:\n  review_assist: true\n  candidate_excerpt_bytes: -1\n  something_else: yes\n")))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		t.Fatalf("retired jev block must parse under the strict decoder: %v", err)
	}
	if raw.Jev != (retiredJev{}) {
		t.Fatalf("tombstone retained content: %#v", raw.Jev)
	}
}

// TestEmptyJevBlockStillParses covers `jev:` with nothing under it, which must
// behave exactly like an absent key.
func TestEmptyJevBlockStillParses(t *testing.T) {
	if _, err := LoadGlobalFromBytes([]byte("jev:\nlog_level: info\n")); err != nil {
		t.Fatalf("empty jev block must parse: %v", err)
	}
}

// TestUnknownTopLevelKeyStillFails pins that the tombstone did not weaken the
// strict decoder anywhere else: a genuinely unknown top-level key is still
// rejected, so the retired block is tolerated by name, not by turning off
// known-fields checking.
func TestUnknownTopLevelKeyStillFails(t *testing.T) {
	_, err := LoadGlobalFromBytes([]byte("not_a_real_key: true\n"))
	if err == nil || !strings.Contains(err.Error(), "not found in type") {
		t.Fatalf("unknown top-level key err = %v, want a known-fields rejection", err)
	}
}
