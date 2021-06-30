/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package v1

import (
	"testing"
)

func Test_setSharedBufferSize(t *testing.T) {

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "default",
			input:    "64Mi",
			expected: "64MB",
		},
		{
			name:     "min",
			input:    "32Mi",
			expected: "32MB",
		},
		{
			name:     "belowMin",
			input:    "31Mi",
			expected: "",
		},
		{
			name:     "Gibibytes",
			input:    "1Gi",
			expected: "1024MB",
		},
		{
			name:     "Kibibytes",
			input:    "102400Ki",
			expected: "100MB",
		},
		{
			name:     "Bytes",
			input:    "67108864",
			expected: "64MB",
		},
		{
			name:     "empty",
			input:    "",
			expected: "",
		},
		{
			name:     "empty2",
			input:    " ",
			expected: "",
		},
		{
			name:     "invalid",
			input:    "64MB",
			expected: "",
		},
	}
	for _, tt := range tests {
		tt := tt // pin!
		t.Run(tt.name, func(t *testing.T) {
			parameters := map[string]string{}

			setSharedBufferSize(parameters, tt.input)

			result, ok := parameters[SharedBufferParameterKey]
			if ok {
				if tt.expected != result {
					t.Errorf("Adapter.ScopedGet() assertion failed: expected %q, got %q\n", tt.expected, result)
				}
			} else {
				if tt.expected != "" {
					t.Errorf("Conversion failed")
				}
			}
		})
	}
}

func TestPostgres_generateTeamID(t *testing.T) {
	tests := []struct {
		name      string
		projectID string
		want      string
	}{
		{
			name:      "beginning with letter",
			projectID: "abc",
			want:      "abc",
		},
		{
			name:      "beginning with number",
			projectID: "1bc",
			want:      "xbc",
		},
		{
			name:      "contains number in the middle",
			projectID: "a1c",
			want:      "a1c",
		},
		{
			name:      "ends with number",
			projectID: "ab1",
			want:      "ab1",
		},
		{
			name:      "contains non-alphanumeric characters",
			projectID: "ab-cd.ef@12",
			want:      "abcdef12",
		},
		{
			name:      "contains non-alphanumeric characters",
			projectID: "12@ab-cd.ef",
			want:      "x2abcdef",
		},
		{
			name:      "more than 16 chars long",
			projectID: "abcdefabcdefabcdefabcdefabcdefab",
			want:      "abcdefabcdefabcd",
		},
		{
			name:      "more than 16 numbers long",
			projectID: "12345678901234567890123456789012",
			want:      "x234567890123456",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Postgres{
				Spec: PostgresSpec{
					ProjectID: tt.projectID,
				},
			}
			if got := p.generateTeamID(); got != tt.want {
				t.Errorf("Postgres.generateTeamID() = %v, want %v", got, tt.want)
			}
		})
	}
}
