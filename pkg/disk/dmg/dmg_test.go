package dmg

import (
	"testing"
)

func TestOpen(t *testing.T) {
	type args struct {
		name string
		c    *Config
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "secure",
			args: args{
				name: "../../../testdata/secure.dmg",
				c:    &Config{Password: "password"},
			},
		},
		{
			name: "test",
			args: args{
				name: "../../../testdata/test.dmg",
				c:    &Config{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Open(tt.args.name, tt.args.c)
			if (err != nil) != tt.wantErr {
				t.Errorf("Open() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != nil {
				defer got.Close()
			}
		})
	}
}
