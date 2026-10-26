package model

import "testing"

func TestLabel(t *testing.T) {
	tests := []struct {
		m    Migration
		want string
	}{
		{Migration{Version: 3, Name: "add_x"}, "0003_add_x"},
		{Migration{Version: 12345, Name: "big"}, "12345_big"},
	}
	for _, tt := range tests {
		if got := tt.m.Label(); got != tt.want {
			t.Errorf("Label() = %q, ожидалось %q", got, tt.want)
		}
	}
}
