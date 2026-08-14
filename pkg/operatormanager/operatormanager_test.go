/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package operatormanager

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestConfigMapDataHash(t *testing.T) {
	tests := []struct {
		name string
		data map[string]string
		want string
	}{
		{
			name: "nil map",
			data: nil,
			want: hashOf(`null`),
		},
		{
			name: "empty map",
			data: map[string]string{},
			want: hashOf(`{}`),
		},
		{
			name: "single key-value pair",
			data: map[string]string{"foo": "bar"},
			want: hashOf(`{"foo":"bar"}`),
		},
		{
			name: "multiple keys",
			data: map[string]string{"foo": "bar", "baz": "qux"},
			want: hashOf(`{"baz":"qux","foo":"bar"}`),
		},
		{
			name: "insertion order does not matter",
			data: map[string]string{"a": "1", "b": "2", "c": "3"},
			want: hashOf(`{"a":"1","b":"2","c":"3"}`),
		},
		{
			name: "empty values are preserved",
			data: map[string]string{"a": "", "b": "x"},
			want: hashOf(`{"a":"","b":"x"}`),
		},
		{
			name: "values containing characters that need JSON escaping are encoded",
			data: map[string]string{"k": "a\"b\nc"},
			want: hashOf(`{"k":"a\"b\nc"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := configMapDataHash(tt.data)
			if got != tt.want {
				t.Errorf("configMapDataHash() = %q, want %q", got, tt.want)
			}
			if _, err := hex.DecodeString(got); err != nil {
				t.Errorf("configMapDataHash() returned non-hex string %q: %v", got, err)
			}
			if len(got) != hex.EncodedLen(sha256.Size) {
				t.Errorf("configMapDataHash() length = %d, want %d", len(got), hex.EncodedLen(sha256.Size))
			}
		})
	}
}

func TestConfigMapDataHash_isOrderIndependent(t *testing.T) {
	a := map[string]string{"x": "1", "y": "2", "z": "3"}
	b := map[string]string{"z": "3", "y": "2", "x": "1"}
	if h1, h2 := configMapDataHash(a), configMapDataHash(b); h1 != h2 {
		t.Errorf("expected identical hashes for maps with same keys, got %q vs %q", h1, h2)
	}
}

func TestConfigMapDataHash_distinctInputsProduceDistinctHashes(t *testing.T) {
	first := configMapDataHash(map[string]string{"a": "1"})
	second := configMapDataHash(map[string]string{"a": "2"})
	if first == second {
		t.Errorf("expected different hashes for different data, both were %q", first)
	}
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
