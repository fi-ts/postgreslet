/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func Test_deriveOwnerData(t *testing.T) {
	tests := []struct {
		name         string
		instanceName string
		want         types.UID
		wantErr      bool
	}{
		{
			name:         "simple",
			instanceName: "foo.bar",
			want:         "bar",
			wantErr:      false,
		},
		{
			name:         "invalidName",
			instanceName: "foo-bar",
			want:         "",
			wantErr:      true,
		},
		{
			name:         "twoDots",
			instanceName: "foo.bar.blah",
			want:         "bar.blah",
			wantErr:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := deriveOwnerData(tt.instanceName)
			if (err != nil) != tt.wantErr {
				t.Errorf("deriveOwnerData() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("deriveOwnerData() got = %v, want %v", got, tt.want)
			}
		})
	}
}
