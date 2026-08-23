package peer

import "testing"

func TestFormatAWGBoolUsesCanonicalToolsSyntax(t *testing.T) {
	if got := FormatAWGBool(true); got != "on" {
		t.Fatalf("FormatAWGBool(true) = %q, want on", got)
	}
	if got := FormatAWGBool(false); got != "off" {
		t.Fatalf("FormatAWGBool(false) = %q, want off", got)
	}
}

func TestParseAWGBoolAcceptsCompatibleForms(t *testing.T) {
	tests := map[string]bool{
		"on": true, "off": false,
		"1": true, "0": false,
		"true": true, "false": false,
		"TRUE": true, "FALSE": false,
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			got, err := ParseAWGBool(input)
			if err != nil {
				t.Fatalf("ParseAWGBool(%q): %v", input, err)
			}
			if got != want {
				t.Fatalf("ParseAWGBool(%q) = %t, want %t", input, got, want)
			}
		})
	}
	if _, err := ParseAWGBool("enabled"); err == nil {
		t.Fatal("ParseAWGBool accepted an invalid value")
	}
}
