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
