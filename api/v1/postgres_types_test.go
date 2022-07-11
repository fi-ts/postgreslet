/*
/ SPDX-FileCopyrightText: 2021 Finanz Informatik Technologie Services GmbHs
/
/ SPDX-License-Identifier: AGPL-1.0-only
*/

package v1

import (
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	}{
		{
			name:      "beginning with letter",
			projectID: "abc",
		},
		{
			name:      "beginning with number",
			projectID: "1bc",
		},
		{
			name:      "contains number in the middle",
			projectID: "a1c",
		},
		{
			name:      "ends with number",
			projectID: "ab1",
		},
		{
			name:      "contains non-alphanumeric characters",
			projectID: "ab-cd.ef@12",
		},
		{
			name:      "contains non-alphanumeric characters",
			projectID: "12@ab-cd.ef",
		},
		{
			name:      "more than 16 chars long",
			projectID: "abcdefabcdefabcdefabcdefabcdefab",
		},
		{
			name:      "more than 16 numbers long",
			projectID: "12345678901234567890123456789012",
		},
	}
	for _, tt := range tests {
		tt := tt // pin!
		t.Run(tt.name, func(t *testing.T) {
			var dnsRegExp *regexp.Regexp = regexp.MustCompile("^[a-z]([-a-z0-9]*[a-z0-9])?$")
			p := &Postgres{
				Spec: PostgresSpec{
					ProjectID: tt.projectID,
				},
			}
			got := p.generateTeamID()
			if !dnsRegExp.MatchString(got) {
				t.Errorf("Postgres.generateTeamID() got %v, not a valid DNS name", got)
			}
		})
	}
}
func TestPostgres_ToPeripheralResourceName(t *testing.T) {
	tests := []struct {
		name         string
		projectID    string
		postgresName string
	}{
		{
			name:         "letters",
			projectID:    "abc",
			postgresName: "bca",
		},
		{
			name:         "numbers at the beginning",
			projectID:    "1bc",
			postgresName: "1cb",
		},
		{
			name:         "numbers in the middle",
			projectID:    "a1c",
			postgresName: "c1a",
		},
		{
			name:         "numbers at the end",
			projectID:    "ab1",
			postgresName: "ba1",
		},
		{
			name:         "non-alphanumeric characters",
			projectID:    "ab-cd.ef@12",
			postgresName: "ba.dc-fe@21",
		},
		{
			name:         "non-alphanumeric characters beginning with numbers",
			projectID:    "12@ab-cd.ef",
			postgresName: "21@ba-dc.fe",
		},
		{
			name:         "too many letters",
			projectID:    "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdef",
			postgresName: "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdef",
		},
		{
			name:         "too many numbers",
			projectID:    "123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890",
			postgresName: "123456789012345678901234567890121234567890123456789012345678901212345678901234567890123456789012",
		},
	}
	for _, tt := range tests {
		tt := tt // pin!
		t.Run(tt.name, func(t *testing.T) {
			var dnsRegExp *regexp.Regexp = regexp.MustCompile("^[a-z]([-a-z0-9]*[a-z0-9])?$")
			p := &Postgres{
				ObjectMeta: v1.ObjectMeta{
					Name: tt.postgresName,
					UID:  types.UID(uuid.NewString()),
				},
				Spec: PostgresSpec{
					ProjectID: tt.projectID,
				},
			}
			got := p.ToPeripheralResourceName()
			if !dnsRegExp.MatchString(got) {
				t.Errorf("Postgres.ToPeripheralResourceName() got %v, not a valid DNS name", got)
			}
			//This resource name will be used as part of the name of other resources, hence we need to limit it's length,
			// e.g. "postgres.bce25ade7552494c-33d21de46d284ea6bec0.credentials"
			maxLen := 37
			if len(got) > maxLen {
				t.Errorf("Postgres.ToPeripheralResourceName() got %v, more than %d characters long", got, maxLen)
			}
		})
	}
}

func TestPostgresRestoreTimestamp_ToUnstructuredZalandoPostgresql(t *testing.T) {
	tests := []struct {
		name             string
		spec             PostgresSpec
		c                *corev1.ConfigMap
		sc               string
		pgParamBlockList map[string]bool
		rbs              *BackupConfig
		srcDB            *Postgres
		want             string
		wantErr          bool
	}{
		{
			name: "empty timestamp initialized with current time",
			spec: PostgresSpec{
				Size: &Size{
					CPU:          "1",
					Memory:       "4Gi",
					SharedBuffer: "64Mi",
				},
				PostgresRestore: &PostgresRestore{
					Timestamp: "",
				},
			},
			c:                nil,
			sc:               "fake-storage-class",
			pgParamBlockList: map[string]bool{},
			rbs:              &BackupConfig{},
			srcDB: &Postgres{
				ObjectMeta: v1.ObjectMeta{
					Name: uuid.NewString(),
				},
				Spec: PostgresSpec{
					Tenant:      "tenant",
					Description: "description",
				},
			},
			want:    time.Now().Format(time.RFC3339), // I know this is not perfect, let's just hope we always finish within the same second...
			wantErr: false,
		},
		{
			name: "undefined timestamp initialized with current time",
			spec: PostgresSpec{
				Size: &Size{
					CPU:          "1",
					Memory:       "4Gi",
					SharedBuffer: "64Mi",
				},
				PostgresRestore: &PostgresRestore{},
			},
			c:                nil,
			sc:               "fake-storage-class",
			pgParamBlockList: map[string]bool{},
			rbs:              &BackupConfig{},
			srcDB: &Postgres{
				ObjectMeta: v1.ObjectMeta{
					Name: uuid.NewString(),
				},
				Spec: PostgresSpec{
					Tenant:      "tenant",
					Description: "description",
				},
			},
			want:    time.Now().Format(time.RFC3339), // I know this is not perfect, let's just hope we always finish within the same second...
			wantErr: false,
		},
		{
			name: "given timestamp is passed along",
			spec: PostgresSpec{
				Size: &Size{
					CPU:          "1",
					Memory:       "4Gi",
					SharedBuffer: "64Mi",
				},
				PostgresRestore: &PostgresRestore{
					Timestamp: "invalid but whatever",
				},
			},
			c:                nil,
			sc:               "fake-storage-class",
			pgParamBlockList: map[string]bool{},
			rbs:              &BackupConfig{},
			srcDB: &Postgres{
				ObjectMeta: v1.ObjectMeta{
					Name: uuid.NewString(),
				},
				Spec: PostgresSpec{
					Tenant:      "tenant",
					Description: "description",
				},
			},
			want:    "invalid but whatever",
			wantErr: false,
		},
		{
			name: "fail on purpose",
			spec: PostgresSpec{
				Size: &Size{
					CPU:          "1",
					Memory:       "4Gi",
					SharedBuffer: "64Mi",
				},
				PostgresRestore: &PostgresRestore{
					Timestamp: "apples",
				},
			},
			c:                nil,
			sc:               "fake-storage-class",
			pgParamBlockList: map[string]bool{},
			rbs:              &BackupConfig{},
			srcDB: &Postgres{
				ObjectMeta: v1.ObjectMeta{
					Name: uuid.NewString(),
				},
				Spec: PostgresSpec{
					Tenant:      "tenant",
					Description: "description",
				},
			},
			want:    "oranges",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt // pin!
		t.Run(tt.name, func(t *testing.T) {
			p := &Postgres{
				Spec: tt.spec,
			}
			got, _ := p.ToUnstructuredZalandoPostgresql(nil, tt.c, tt.sc, tt.pgParamBlockList, tt.rbs, tt.srcDB, 130, 10, 60)

			jsonZ, err := runtime.DefaultUnstructuredConverter.ToUnstructured(got)
			if err != nil {
				t.Errorf("failed to convert to unstructured zalando postgresql: %v", err)
			}
			jsonSpec, _ := jsonZ["spec"].(map[string]interface{})
			jsonClone, _ := jsonSpec["clone"].(map[string]interface{})

			if !tt.wantErr && tt.want != jsonClone["timestamp"] {
				t.Errorf("Spec.Clone.Timestamp was %v, but expected %v", jsonClone["timestamp"], tt.want)
			}
		})
	}
}
